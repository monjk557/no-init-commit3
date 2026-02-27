package protocol

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/protocol/gitcmd"
)

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
	Wants      []string
	Haves      []string
	Done       bool
	ThinPack   bool
	OfsDelta   bool
	IncludeTag bool
	NoProgress bool
	// Shallow/Deepen
	Shallow     []string
	Deepen      int
	DeepenSince int64
	DeepenNot   []string
	// Filter
	Filter      string // e.g. "blob:none", "tree:0"
	SidebandAll bool
	// PackfileURIs 등은 미지원이라면 필드만 두고 무시 처리
	PackfileURIs bool
}

// DefaultPackWindow is the delta search window size for packfile encoding.
// Non-zero enables delta compression (smaller packs, faster transfer). Git default is 10.
const DefaultPackWindow uint = 10

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
	// v2 advertisement 포맷: 각 라인은 \n으로 끝나고, 마지막에 0000 flush
	pw := &PktWriter{W: w, WithLF: true}
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

// WriteV1Advert writes v1 protocol ref advertisement using native git commands.
// repoPath is the filesystem path to the bare repository.
func WriteV1Advert(w io.Writer, repoPath string, caps ...string) error {
	pw := &PktWriter{W: w, WithLF: false}
	ctx := context.Background()

	// List refs using native git
	refs, err := gitcmd.ListRefs(ctx, repoPath, "refs/heads/", "refs/tags/")
	if err != nil {
		return fmt.Errorf("list refs: %w", err)
	}

	// Resolve HEAD
	var headTarget string
	headInfo, err := gitcmd.ResolveHEAD(ctx, repoPath)
	if err == nil && headInfo.IsSymbolic {
		headTarget = headInfo.Target
	}

	type advRef struct {
		name string
		oid  string
		kind int // 0=heads, 1=tags
	}
	var lst []advRef

	for _, ref := range refs {
		var k int
		if strings.HasPrefix(ref.RefName, "refs/heads/") {
			k = 0
		} else if strings.HasPrefix(ref.RefName, "refs/tags/") {
			k = 1
		} else {
			continue
		}
		lst = append(lst, advRef{name: ref.RefName, oid: ref.ObjectName, kind: k})
	}

	// Empty repo: send zero-id capabilities line + flush.
	// Git v1 protocol requires at least one pkt-line with capabilities
	// even when there are no refs. Without this, the client can't negotiate
	// report-status and the push appears to fail (SSH exit code 1).
	if len(lst) == 0 {
		var capStr string
		if len(caps) > 0 {
			capStr = strings.Join(caps, " ")
		}
		zeroOID := "0000000000000000000000000000000000000000"
		firstLine := fmt.Sprintf("%s capabilities^{}\x00%s\n", zeroOID, capStr)
		if err := pw.WriteLine(firstLine); err != nil {
			return fmt.Errorf("write empty-repo advert: %w", err)
		}
		return pw.Flush()
	}

	// Sort: HEAD target first, then heads before tags, then alphabetically
	sort.SliceStable(lst, func(i, j int) bool {
		if headTarget != "" {
			if lst[i].name == headTarget && lst[j].name != headTarget {
				return true
			}
			if lst[j].name == headTarget && lst[i].name != headTarget {
				return false
			}
		}
		if lst[i].kind != lst[j].kind {
			return lst[i].kind < lst[j].kind
		}
		return lst[i].name < lst[j].name
	})

	// Build capabilities: caller caps + symref=HEAD:<target>
	cset := make(map[string]struct{}, len(caps)+2)
	var merged []string
	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := cset[c]; ok {
			continue
		}
		cset[c] = struct{}{}
		merged = append(merged, c)
	}
	if headTarget != "" {
		sym := "symref=HEAD:" + headTarget
		if _, ok := cset[sym]; !ok {
			merged = append(merged, sym)
		}
	}

	// First line: "<oid> <ref>\x00cap1 cap2 ...\n"
	first := lst[0]
	firstLine := fmt.Sprintf("%s %s\x00%s\n", first.oid, first.name, strings.Join(merged, " "))
	if err := pw.WriteLine(firstLine); err != nil {
		return fmt.Errorf("write first advert: %w", err)
	}

	// Remaining lines
	for _, r := range lst[1:] {
		if err := pw.WriteLine(fmt.Sprintf("%s %s\n", r.oid, r.name)); err != nil {
			return fmt.Errorf("write advert line: %w", err)
		}
	}

	return pw.Flush()
}

// 사용 예:
// upload-pack: caps := []string{"ls-refs", "fetch"}
// receive-pack: caps := []string{"receive-pack"}

func ParseLsRefsSection(cmds []string) (*LsRefsReq, error) {
	r := &LsRefsReq{}
	for _, raw := range cmds {
		// Trim whitespace and newlines from command (same as ParseFetchSection)
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}

		switch {
		case c == "command=ls-refs":
			// Command identifier, no action needed

		case strings.HasPrefix(c, "agent="):
			r.Agent = strings.TrimSpace(strings.TrimPrefix(c, "agent="))

		case c == "symrefs":
			r.Symrefs = true

		case c == "peel":
			r.Peel = true

		case strings.HasPrefix(c, "ref-prefix "):
			prefix := strings.TrimSpace(c[11:])
			if prefix != "" {
				r.RefPrefix = append(r.RefPrefix, prefix)
			}

		case c == "unborn":
			r.Unborn = true

		default:
			// 알 수 없는 옵션은 무시 (Git protocol v2 확장성을 위해)
		}
	}

	return r, nil
}

func ParseFetchSection(cmds []string) (*FetchReq, error) {
	r := &FetchReq{}
	for _, raw := range cmds {
		c := strings.TrimSpace(raw)
		switch {
		case c == "command=fetch":
		case strings.HasPrefix(c, "agent="):
			r.Agent = strings.TrimPrefix(c, "agent=")

		case strings.HasPrefix(c, "want "):
			r.Wants = append(r.Wants, strings.TrimSpace(c[5:]))
		case strings.HasPrefix(c, "have "):
			r.Haves = append(r.Haves, strings.TrimSpace(c[5:]))
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
			r.Shallow = append(r.Shallow, strings.TrimSpace(c[8:]))
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

// ValidateFetchSupport validates that the fetch request uses supported options.
// With native git, we now support deepen-since, deepen-not, shallow, and filter.
// Only sideband-all and packfile-uris remain unsupported.
func ValidateFetchSupport(r *FetchReq) error {
	if r.SidebandAll {
		return fmt.Errorf("sideband-all not supported")
	}
	if r.PackfileURIs {
		return fmt.Errorf("packfile-uris not supported")
	}
	return nil
}

// GetShallowBoundary computes shallow boundary using native git rev-list --boundary.
func GetShallowBoundary(repoPath string, r *FetchReq) ([]string, error) {
	if r == nil || r.Deepen <= 0 {
		return nil, nil
	}
	ctx := context.Background()
	return gitcmd.GetShallowBoundary(ctx, repoPath, r.Wants, r.Haves, r.Deepen)
}
