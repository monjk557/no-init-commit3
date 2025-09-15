package protocol

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/git"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// packfile 프로토콜 작성을 위한 라이브러리
type HashIter struct {
	St     storer.EncodedObjectStorer
	Hashes []plumbing.Hash
	i      int
}

type PktWriter struct {
	W      io.Writer
	WithLF bool // true면 payload 끝에 '\n' 붙이고 길이에 포함
}

type LsRefsReq struct {
	Agent     string
	Symrefs   bool
	Peel      bool
	RefPrefix []string // e.g. HEAD, refs/heads/, refs/tags/
	Unborn    bool     // (선택) 미지원이면 무시
}

type FetchReq struct {
	Agent      string
	Wants      []plumbing.Hash
	Haves      []plumbing.Hash
	Done       bool
	ThinPack   bool
	OfsDelta   bool
	IncludeTag bool
	NoProgress bool
	// Shallow/Deepen
	Shallow     []plumbing.Hash
	Deepen      int
	DeepenSince int64
	DeepenNot   []string
	// Filter
	Filter      string // e.g. "blob:none", "tree:0"
	SidebandAll bool
	// PackfileURIs 등은 미지원이라면 필드만 두고 무시 처리
	PackfileURIs bool
}

func (it *HashIter) Next() (plumbing.EncodedObject, error) {
	if it.i >= len(it.Hashes) {
		return nil, io.EOF
	}
	obj, err := it.St.EncodedObject(plumbing.AnyObject, it.Hashes[it.i])
	it.i++
	return obj, err
}
func (it *HashIter) ForEach(cb func(plumbing.EncodedObject) error) error {
	for {
		obj, err := it.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := cb(obj); err != nil {
			return err
		}
	}
}

func WritePackfile(w io.Writer, st storer.EncodedObjectStorer, hashes []plumbing.Hash) error {

	// Use go-git's packfile encoder with the correct signature
	enc := packfile.NewEncoder(w, st, true) // ref-delta 사용 여부

	// The encoder.Encode method expects []plumbing.Hash, not []*ObjectToPack
	_, err := enc.Encode(hashes, 0)
	if err != nil {
		return err
	}

	return nil
}

// "packfile" 섹션 헤더 (pkt-line + flush)
func WritePackfileSectionHeader(w io.Writer) error {
	pw := &PktWriter{W: w, WithLF: false} // LF 안 붙임(길이 혼동 방지)
	if err := pw.WriteLine("packfile"); err != nil {
		return err
	}
	return nil // 0000
}

// pkt-line 프로토콜 작성을 위한 라이브러리
// pkt-line 프로토콜은 Git 프로토콜의 기본 형식으로, 데이터를 전송하는 데 사용된다.

func (p *PktWriter) WriteLine(s string) error {
	payload := s
	if p.WithLF {
		payload += "\n"
	}
	_, err := fmt.Fprintf(p.W, "%04x%s", len(payload)+4, payload)
	return err
}

func (p *PktWriter) Flush() error {
	_, err := p.W.Write([]byte("0000"))
	return err
}

func (p *PktWriter) Err(msg string) error {
	return p.WriteLine("ERR " + msg)
}

// Git V2 프로토콜 광고 작성을 위한 라이브러리
// 프로토콜 광고란, 클라이언트가 지원하는 기능을 알려주는 메시지

func WriteV2Advert(w io.Writer, agent string, caps ...string) error {
	pw := &PktWriter{W: w, WithLF: false} // 광고는 사람 읽기 편하게 LF 포함으로 통일해도 OK
	if err := pw.WriteLine("version 2"); err != nil {
		return err
	}
	if agent != "" {
		if err := pw.WriteLine("agent=" + agent); err != nil {
			return err
		}
	}
	for _, c := range caps {
		if err := pw.WriteLine(c); err != nil {
			return err
		}
	}
	return pw.Flush()
}

// 사용 예:
// upload-pack: caps := []string{"ls-refs", "fetch"}
// receive-pack: caps := []string{"receive-pack"}

func ParseLsRefsSection(cmds []string) (*LsRefsReq, error) {
	r := &LsRefsReq{}
	for _, c := range cmds {
		switch {
		case c == "command=ls-refs":
		case strings.HasPrefix(c, "agent="):
			r.Agent = strings.TrimPrefix(c, "agent=")
		case c == "symrefs":
			r.Symrefs = true
		case c == "peel":
			r.Peel = true
		case strings.HasPrefix(c, "ref-prefix "):
			prefix := strings.TrimSpace(c[11:])
			r.RefPrefix = append(r.RefPrefix, prefix)
		case c == "unborn":
			r.Unborn = true
		default:
			// 알 수 없는 옵션은 무시
		}
	}
	return r, nil
}

func ParseFetchSection(cmds []string) (*FetchReq, error) {
	r := &FetchReq{}
	for _, c := range cmds {
		switch {
		case c == "command=fetch":
		case strings.HasPrefix(c, "agent="):
			r.Agent = strings.TrimPrefix(c, "agent=")

		case strings.HasPrefix(c, "want "):
			r.Wants = append(r.Wants, plumbing.NewHash(strings.TrimSpace(c[5:])))
		case strings.HasPrefix(c, "have "):
			r.Haves = append(r.Haves, plumbing.NewHash(strings.TrimSpace(c[5:])))
		case c == "done":
			r.Done = true

		case c == "thin-pack":
			r.ThinPack = true
		case c == "ofs-delta":
			r.OfsDelta = true
		case c == "include-tag":
			r.IncludeTag = true
		case c == "no-progress":
			r.NoProgress = true

		case strings.HasPrefix(c, "shallow "):
			r.Shallow = append(r.Shallow, plumbing.NewHash(strings.TrimSpace(c[8:])))
		case strings.HasPrefix(c, "deepen "):
			n, _ := strconv.Atoi(strings.TrimSpace(c[7:]))
			r.Deepen = n
		case strings.HasPrefix(c, "deepen-since "):
			ts, _ := strconv.ParseInt(strings.TrimSpace(c[13:]), 10, 64)
			r.DeepenSince = ts
		case strings.HasPrefix(c, "deepen-not "):
			r.DeepenNot = append(r.DeepenNot, strings.TrimSpace(c[11:]))

		case strings.HasPrefix(c, "filter "):
			r.Filter = strings.TrimSpace(c[7:])
		case c == "sideband-all":
			r.SidebandAll = true

		case strings.HasPrefix(c, "packfile-uris"):
			r.PackfileURIs = true

		default:
			// 알 수 없는 옵션은 무시
		}
	}
	return r, nil
}

func ParsePushSection(cmds []string) error {

	return nil
}

// 옵션 검증/정책: 지원 불가 옵션 앞단에서 차단(섹션 헤더 전 ERR)
// 사용처: fetch 처리 진입 직전에 호출
func ValidateFetchSupport(r *FetchReq) error {
	// 예: MVP에서는 filter/shallow/deepen/sideband-all/packfile-uris 미지원
	if r.Filter != "" {
		return errors.New("filter not supported")
	}
	if r.Deepen > 0 || r.DeepenSince > 0 || len(r.DeepenNot) > 0 || len(r.Shallow) > 0 {
		return errors.New("shallow/deepen not supported")
	}
	if r.SidebandAll {
		return errors.New("sideband-all not supported")
	}
	if r.PackfileURIs {
		return errors.New("packfile-uris not supported")
	}
	return nil
}

// fetch 대상 해시 집합 계산 (revlist)
// - wants/haves 기반으로 전송해야 할 객체 목록을 만든다
// - include-tag 지원: wants와 연관된 annotated tag를 찾아 추가(선택)
func BuildFetchObjectList(st storer.EncodedObjectStorer, repo git.Repository, r *FetchReq) ([]plumbing.Hash, error) {
	objs, err := revlist.Objects(st, r.Wants, r.Haves)
	if err != nil {
		return nil, fmt.Errorf("revlist.Objects failed: %w", err)
	}

	if r.IncludeTag {
		tagHashes, _ := collectRelatedTags(repo, r.Wants)
		if len(tagHashes) > 0 {
			// objs와 합집합
			set := map[plumbing.Hash]struct{}{}
			for _, h := range objs {
				set[h] = struct{}{} // ✅ 올바른 구문
			}
			for _, th := range tagHashes {
				set[th] = struct{}{} // ✅ 올바른 구문
			}
			merged := make([]plumbing.Hash, 0, len(set))
			for h := range set {
				merged = append(merged, h)
			}
			objs = merged
		}
	}

	// 정렬(안 해도 되지만, 테스트 재현성을 위해)
	sort.Slice(objs, func(i, j int) bool { return objs[i].String() < objs[j].String() })
	return objs, nil
}

// wants와 연결된 annotated tags 찾아 추가 (간단 구현)
func collectRelatedTags(repo git.Repository, wants []plumbing.Hash) ([]plumbing.Hash, error) {
	if len(wants) == 0 {
		return nil, nil
	}
	wantSet := map[plumbing.Hash]struct{}{}
	for _, w := range wants {
		wantSet[w] = struct{}{} // ✅ 올바른 구문
	}

	// Get the underlying go-git repository
	goGitRepo, ok := repo.(*git.GoGitRepository)
	if !ok {
		// If it's not a GoGitRepository, we can't access references
		// Return empty result for now
		return nil, nil
	}

	underlyingRepo := goGitRepo.GetGoGitRepo()
	refs, err := underlyingRepo.References()
	if err != nil {
		return nil, err
	}
	defer refs.Close()

	var out []plumbing.Hash
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if !strings.HasPrefix(ref.Name().String(), "refs/tags/") {
			return nil
		}
		tagObj, e := underlyingRepo.TagObject(ref.Hash())
		if e != nil || tagObj == nil {
			return nil // lightweight tag
		}
		if _, ok := wantSet[tagObj.Target]; ok {
			out = append(out, ref.Hash())
		}
		return nil
	})
	return out, nil
}

// (선택) deepen-since 값을 time.Time으로 변환할 때 사용
func DeepenSinceTime(unix int64) time.Time { return time.Unix(unix, 0).UTC() }
