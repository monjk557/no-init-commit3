package protocol

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// packfile 프로토콜 작성을 위한 라이브러리
type HashIter struct {
	St     storer.EncodedObjectStorer
	Hashes []plumbing.Hash
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

// Git V1 프로토콜 광고 작성을 위한 펑션
func WriteV1Advert(w io.Writer, repo gogit.Repository, caps ...string) error {
	pw := &PktWriter{W: w, WithLF: false}

	// refs 수집 (루스 + packed 모두 포함)
	it, err := repo.References()
	if err != nil {
		return fmt.Errorf("iter refs: %w", err)
	}
	defer it.Close()

	type advRef struct {
		name string
		oid  plumbing.Hash
		kind int // 0=heads, 1=tags, 2=others
	}
	var lst []advRef

	var headTarget string
	// HEAD 심볼릭 대상
	if headRef, err := repo.Storer.Reference(plumbing.HEAD); err == nil && headRef.Type() == plumbing.SymbolicReference {
		headTarget = headRef.Target().String()
	}

	// 로직설명:
	// 1. 모든 ref를 순회하며, ref의 타입에 따라 분류
	// 2. ref의 타입이 head 이면 0
	// 3. ref의 타입이 tag 이면 1
	// 4. ref의 타입이 나머지 이면 2
	err = it.ForEach(func(r *plumbing.Reference) error {
		switch r.Type() {
		case plumbing.HashReference:
			n := r.Name().String()
			var k int
			if strings.HasPrefix(n, "refs/heads/") {
				k = 0
			} else if strings.HasPrefix(n, "refs/tags/") {
				k = 1
			} else {
				// receive-pack 광고에서 remotes/*는 보통 제외
				return nil
			}
			lst = append(lst, advRef{name: n, oid: r.Hash(), kind: k})
		case plumbing.SymbolicReference:
			// 심볼릭 라인은 광고 리스트에 넣지 않음 (HEAD는 capability로만 알림)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("foreach refs: %w", err)
	}

	// 빈 저장소: 그냥 flush
	if len(lst) == 0 {
		return pw.Flush()
	}

	// HEAD 타깃(refs/heads/main 등)을 첫 줄로 오게 정렬 (없으면 heads 우선)
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

	// capabilities 구성: 호출자가 준 caps + symref=HEAD:<target>(가능하면) 중복 제거
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

	// 첫 줄: "<oid> <ref>\x00cap1 cap2 ...\n"
	first := lst[0]
	firstLine := fmt.Sprintf("%s %s\x00%s\n", first.oid.String(), first.name, strings.Join(merged, " "))
	if err := pw.WriteLine(firstLine); err != nil {
		return fmt.Errorf("write first advert: %w", err)
	}

	// 나머지 라인
	for _, r := range lst[1:] {
		if err := pw.WriteLine(fmt.Sprintf("%s %s\n", r.oid.String(), r.name)); err != nil {
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

// 옵션 검증/정책: 지원 불가 옵션 앞단에서 차단(섹션 헤더 전 ERR)
// 사용처: fetch 처리 진입 직전에 호출
func ValidateFetchSupport(r *FetchReq) error {
	// 예: MVP에서는 filter/shallow/deepen/sideband-all/packfile-uris 미지원
	if r.Filter != "" {
		return errors.New("filter not supported")
	}
	// deepen-since, deepen-not, client shallow: 미지원. deepen N만 지원.
	if r.DeepenSince > 0 || len(r.DeepenNot) > 0 || len(r.Shallow) > 0 {
		return errors.New("deepen-since/deepen-not/shallow not supported")
	}
	if r.SidebandAll {
		return errors.New("sideband-all not supported")
	}
	if r.PackfileURIs {
		return errors.New("packfile-uris not supported")
	}
	return nil
}

// GetShallowBoundary returns shallow boundary hashes when FetchReq has Deepen > 0, without
// enumerating the full object list. Used by TCP upload-pack to avoid BuildFetchObjectList
// on the pack-objects success path.
func GetShallowBoundary(repo *gogit.Repository, r *FetchReq) ([]plumbing.Hash, error) {
	if r == nil || r.Deepen <= 0 {
		return nil, nil
	}
	_, boundary, err := depthLimitedCommits(repo, r.Wants, r.Deepen)
	return boundary, err
}

// depthLimitedCommits returns commits to include (depth 0..maxDepth-1) and shallow boundary (commits at maxDepth-1).
// Git "deepen N" = N levels of history: level 0 = wants, level 1 = parents, ...; we include levels 0..N-1.
func depthLimitedCommits(repo *gogit.Repository, wants []plumbing.Hash, maxDepth int) (included, boundary []plumbing.Hash, err error) {
	if maxDepth <= 0 || len(wants) == 0 {
		return wants, nil, nil
	}
	type item struct {
		hash  plumbing.Hash
		depth int
	}
	queue := make([]item, 0, len(wants)+64)
	for _, w := range wants {
		queue = append(queue, item{w, 0})
	}
	seen := make(map[plumbing.Hash]struct{})
	included = make([]plumbing.Hash, 0, len(wants)*2)
	boundary = make([]plumbing.Hash, 0, len(wants))

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, ok := seen[cur.hash]; ok {
			continue
		}
		seen[cur.hash] = struct{}{}
		if cur.depth >= maxDepth {
			continue
		}
		included = append(included, cur.hash)
		if cur.depth == maxDepth-1 {
			boundary = append(boundary, cur.hash)
		}
		commit, err := repo.CommitObject(cur.hash)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			queue = append(queue, item{p, cur.depth + 1})
		}
	}
	return included, boundary, nil
}

// BuildFetchObjectListResult holds the object list and optional shallow boundary for fetch.
type BuildFetchObjectListResult struct {
	Objects         []plumbing.Hash
	ShallowBoundary []plumbing.Hash
}

// fetch 대상 해시 집합 계산 (revlist)
// - wants/haves 기반으로 전송해야 할 객체 목록을 만든다
// - deepen N 이면 depth 제한 후 boundary를 ShallowBoundary로 반환
// - include-tag 지원: wants와 연관된 annotated tag를 찾아 추가(선택)
func BuildFetchObjectList(_ storer.EncodedObjectStorer, repo *gogit.Repository, r *FetchReq) (*BuildFetchObjectListResult, error) {
	var wantCommits []plumbing.Hash
	var shallowBoundary []plumbing.Hash
	haveExclusions := append([]plumbing.Hash(nil), r.Haves...)

	// Clone (no haves, no deepen): enumerate all objects via sequential iterator.
	// BFS 방식은 object 수만큼 random pack read → Docker volume에서 매우 느림.
	// storer iterator는 pack 파일을 순차 읽어 훨씬 빠름.
	if len(haveExclusions) == 0 && r.Deepen == 0 {
		objs, err := collectAllObjectsViaIterator(repo)
		if err != nil {
			return nil, fmt.Errorf("collectAllObjectsViaIterator: %w", err)
		}
		return &BuildFetchObjectListResult{Objects: objs}, nil
	}

	if r.Deepen > 0 {
		included, boundary, err := depthLimitedCommits(repo, r.Wants, r.Deepen)
		if err != nil {
			return nil, fmt.Errorf("depthLimitedCommits: %w", err)
		}
		wantCommits = included
		shallowBoundary = boundary

		// shallow clone: revlist.Objects는 repo 전체를 이터레이션하므로 직접 tree walk.
		objs, err := collectShallowObjects(repo, wantCommits)
		if err != nil {
			return nil, fmt.Errorf("collectShallowObjects: %w", err)
		}

		sort.Slice(objs, func(i, j int) bool { return objs[i].String() < objs[j].String() })
		return &BuildFetchObjectListResult{Objects: objs, ShallowBoundary: shallowBoundary}, nil
	}

	wantCommits = r.Wants

	// revlist.Objects 는 go-git 내부 filesystem 레이어(DotGit.ObjectPack)에서
	// push 직후 stale packfile 상태 시 panic 발생. repo.CommitObject/TreeObject를
	// 직접 순회하는 collectAllObjects 로 대체.
	objs, err := collectAllObjects(repo, wantCommits, haveExclusions)
	if err != nil {
		return nil, fmt.Errorf("collectAllObjects failed: %w", err)
	}

	if r.IncludeTag {
		tagHashes, _ := collectRelatedTags(repo, wantCommits)
		if len(tagHashes) > 0 {
			set := map[plumbing.Hash]struct{}{}
			for _, h := range objs {
				set[h] = struct{}{}
			}
			for _, th := range tagHashes {
				set[th] = struct{}{}
			}
			merged := make([]plumbing.Hash, 0, len(set))
			for h := range set {
				merged = append(merged, h)
			}
			objs = merged
		}
	}

	sort.Slice(objs, func(i, j int) bool { return objs[i].String() < objs[j].String() })
	return &BuildFetchObjectListResult{Objects: objs, ShallowBoundary: shallowBoundary}, nil
}

// collectAllObjectsViaIterator enumerates every object in the repository using
// the storer's sequential iterator. Clone(have 없음) 전용 최적화 경로.
// BFS 기반 collectAllObjects 는 object 수만큼 random pack seek 이 필요하지만
// storer.IterEncodedObjects 는 pack 을 순차로 읽어 Docker volume 에서 훨씬 빠름.
func collectAllObjectsViaIterator(repo *gogit.Repository) ([]plumbing.Hash, error) {
	iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return nil, fmt.Errorf("failed to iter objects: %w", err)
	}
	defer iter.Close()

	var result []plumbing.Hash
	err = iter.ForEach(func(obj plumbing.EncodedObject) error {
		result = append(result, obj.Hash())
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("iter objects: %w", err)
	}
	return result, nil
}

// collectAllObjects walks all commits reachable from wants (following parents),
// excluding commits reachable from excludes. revlist.Objects 대신 사용.
// revlist.Objects 는 go-git DotGit.ObjectPack 에서 push 직후 panic 가능.
func collectAllObjects(repo *gogit.Repository, wants []plumbing.Hash, excludes []plumbing.Hash) ([]plumbing.Hash, error) {
	// Step 1: exclude 측 commit 집합 구축 (BFS).
	// 클라이언트가 이미 가진 커밋에서 도달 가능한 커밋은 전송 불필요.
	excludedCommits := make(map[plumbing.Hash]struct{}, len(excludes)*8)
	if len(excludes) > 0 {
		eq := make([]plumbing.Hash, 0, len(excludes))
		for _, h := range excludes {
			if _, ok := excludedCommits[h]; !ok {
				excludedCommits[h] = struct{}{}
				eq = append(eq, h)
			}
		}
		for len(eq) > 0 {
			h := eq[0]
			eq = eq[1:]
			c, err := repo.CommitObject(h)
			if err != nil {
				continue
			}
			for _, p := range c.ParentHashes {
				if _, ok := excludedCommits[p]; !ok {
					excludedCommits[p] = struct{}{}
					eq = append(eq, p)
				}
			}
		}
	}

	seen := make(map[plumbing.Hash]struct{})
	result := make([]plumbing.Hash, 0, 256)

	add := func(h plumbing.Hash) bool {
		if _, ok := seen[h]; ok {
			return false
		}
		seen[h] = struct{}{}
		result = append(result, h)
		return true
	}

	var walkTree func(treeHash plumbing.Hash) error
	walkTree = func(treeHash plumbing.Hash) error {
		if !add(treeHash) {
			return nil
		}
		tree, err := repo.TreeObject(treeHash)
		if err != nil {
			return fmt.Errorf("tree %s: %w", treeHash, err)
		}
		for _, entry := range tree.Entries {
			switch entry.Mode {
			case filemode.Dir:
				if err := walkTree(entry.Hash); err != nil {
					return err
				}
			default:
				add(entry.Hash)
			}
		}
		return nil
	}

	// Step 2: want 측 커밋 BFS.
	cq := make([]plumbing.Hash, 0, len(wants))
	for _, w := range wants {
		if _, excl := excludedCommits[w]; excl {
			continue
		}
		if add(w) {
			cq = append(cq, w)
		}
	}

	for len(cq) > 0 {
		h := cq[0]
		cq = cq[1:]
		c, err := repo.CommitObject(h)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", h, err)
		}
		if err := walkTree(c.TreeHash); err != nil {
			return nil, err
		}
		for _, p := range c.ParentHashes {
			if _, excl := excludedCommits[p]; excl {
				continue
			}
			if add(p) {
				cq = append(cq, p)
			}
		}
	}

	return result, nil
}

// collectShallowObjects walks only the direct tree of each commit (no parent traversal).
// revlist.Objects는 repo 전체 object를 스캔하므로 shallow clone에서 사용 불가.
func collectShallowObjects(repo *gogit.Repository, commits []plumbing.Hash) ([]plumbing.Hash, error) {
	seen := make(map[plumbing.Hash]struct{})
	result := make([]plumbing.Hash, 0, len(commits)*64)

	add := func(h plumbing.Hash) bool {
		if _, ok := seen[h]; ok {
			return false
		}
		seen[h] = struct{}{}
		result = append(result, h)
		return true
	}

	var walkTree func(treeHash plumbing.Hash) error
	walkTree = func(treeHash plumbing.Hash) error {
		if !add(treeHash) {
			// 이미 방문한 tree → 내용도 이미 처리됨
			return nil
		}
		tree, err := repo.TreeObject(treeHash)
		if err != nil {
			return fmt.Errorf("tree %s: %w", treeHash, err)
		}
		for _, entry := range tree.Entries {
			switch entry.Mode {
			case filemode.Dir:
				// subtree: walkTree가 add + 재귀를 담당. 여기서 pre-add하면
				// walkTree 진입시 already-seen으로 early return해서 내용이 누락됨.
				if err := walkTree(entry.Hash); err != nil {
					return err
				}
			default:
				// blob, symlink, executable, submodule(gitlink) - 해시만 추가
				add(entry.Hash)
			}
		}
		return nil
	}

	for _, commitHash := range commits {
		if !add(commitHash) {
			continue
		}
		commit, err := repo.CommitObject(commitHash)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", commitHash, err)
		}
		if err := walkTree(commit.TreeHash); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// wants와 연결된 annotated tags 찾아 추가 (간단 구현)
func collectRelatedTags(repo *gogit.Repository, wants []plumbing.Hash) ([]plumbing.Hash, error) {
	if len(wants) == 0 {
		return nil, nil
	}
	wantSet := map[plumbing.Hash]struct{}{}
	for _, w := range wants {
		wantSet[w] = struct{}{} // ✅ 올바른 구문
	}

	// Get the underlying go-git repository
	underlyingRepo := repo
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
		tagObj, e := repo.TagObject(ref.Hash())
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
