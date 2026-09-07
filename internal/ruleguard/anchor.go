package ruleguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// anchor.go — 신뢰 앵커를 요청 단위로 만든다.
//
// git 은 앵커를 파일 경로로만 받는다. 그래서 번들의 공개키를 그때그때 파일로 떨어뜨려
// 넘기고 요청이 끝나면 지운다. 저장소나 전역 gitconfig 를 건드리지 않으므로 동시 push
// 가 서로의 앵커를 덮어쓰는 사고가 생기지 않는다.
//
// 형식마다 앵커의 모양이 다르다. ssh 는 allowed_signers 텍스트 한 장이고, gpg 와
// x509 는 임시 GNUPGHOME 에 공개키를 import 한 키링이다. 판정 자체는 세 형식 모두
// git 이 하므로 여기서 갈리는 것은 준비 과정뿐이다.

// Anchor 는 git 에 넘길 앵커다. 다 쓰면 Cleanup 을 부른다.
type Anchor struct {
	// GitArgs 는 git 앞에 붙일 -c 설정들이다.
	GitArgs []string
	// Env 는 git 에 넘길 추가 환경변수다 (GNUPGHOME).
	Env []string

	cleanup func()
}

// Cleanup 은 앵커가 만든 임시 파일을 지운다. nil Anchor 에도 안전하다.
func (a *Anchor) Cleanup() {
	if a == nil || a.cleanup == nil {
		return
	}
	a.cleanup()
}

// BuildAnchor 는 signers 로 앵커를 만든다.
//
// 키가 하나도 없으면 nil 을 돌려준다. 앵커 없이 물으면 유효한 서명도 "서명 없음" 으로
// 나오므로, 호출부는 그 경우 서명 사실을 만들지 않아야 한다.
func BuildAnchor(ctx context.Context, signers []BundleSigner) (*Anchor, error) {
	var sshLines []string
	var keyMaterial []string

	for _, s := range signers {
		if s.PublicKey == "" {
			continue
		}
		switch s.KeyFormat {
		case "ssh":
			// allowed_signers 한 줄 = "<principal> <keytype> <base64>".
			// principal 은 이메일이며, 없는 키는 committer 대조를 할 수 없으므로 싣지 않는다.
			for _, email := range s.Emails {
				sshLines = append(sshLines, email+" "+s.PublicKey)
			}
		case "gpg", "x509":
			keyMaterial = append(keyMaterial, s.PublicKey)
		}
	}

	if len(sshLines) == 0 && len(keyMaterial) == 0 {
		return nil, nil
	}

	dir, err := os.MkdirTemp("", "gitsig-anchor-")
	if err != nil {
		return nil, fmt.Errorf("anchor tempdir: %w", err)
	}
	anchor := &Anchor{cleanup: func() { _ = os.RemoveAll(dir) }}

	if len(sshLines) > 0 {
		path := filepath.Join(dir, "allowed_signers")
		if err := os.WriteFile(path, []byte(strings.Join(sshLines, "\n")+"\n"), 0o600); err != nil {
			anchor.Cleanup()
			return nil, fmt.Errorf("anchor allowed_signers: %w", err)
		}
		anchor.GitArgs = append(anchor.GitArgs, "-c", "gpg.ssh.allowedSignersFile="+path)
	}

	if len(keyMaterial) > 0 {
		home := filepath.Join(dir, "gnupg")
		if err := os.Mkdir(home, 0o700); err != nil {
			anchor.Cleanup()
			return nil, fmt.Errorf("anchor gnupghome: %w", err)
		}
		if err := importKeys(ctx, home, keyMaterial); err != nil {
			anchor.Cleanup()
			return nil, err
		}
		anchor.Env = append(anchor.Env, "GNUPGHOME="+home)
	}

	return anchor, nil
}

// importKeys 는 공개키를 임시 키링에 넣고 신뢰를 지정한다.
//
// import 만 하면 git 이 U(유효하나 신뢰 미지정)로 답한다. 우리 기준에서 번들에 실려
// 온 키는 등록된 키이므로, ownertrust 를 주어 G 로 통일한다. 그래야 ssh 와 판정
// 글자가 같아지고 호출부가 형식을 구분하지 않아도 된다.
func importKeys(ctx context.Context, home string, keys []string) error {
	imp := exec.CommandContext(ctx, "gpg", "--batch", "--quiet", "--import")
	imp.Env = append(os.Environ(), "GNUPGHOME="+home)
	imp.Stdin = strings.NewReader(strings.Join(keys, "\n"))
	if out, err := imp.CombinedOutput(); err != nil {
		return fmt.Errorf("anchor gpg import: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	fprs, err := listFingerprints(ctx, home)
	if err != nil || len(fprs) == 0 {
		return err
	}

	var trust strings.Builder
	for _, fpr := range fprs {
		trust.WriteString(fpr + ":6:\n")
	}
	tr := exec.CommandContext(ctx, "gpg", "--batch", "--import-ownertrust")
	tr.Env = append(os.Environ(), "GNUPGHOME="+home)
	tr.Stdin = strings.NewReader(trust.String())
	if out, err := tr.CombinedOutput(); err != nil {
		return fmt.Errorf("anchor gpg ownertrust: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// listFingerprints 는 방금 넣은 키들의 지문을 읽는다. ownertrust 는 지문으로만 준다.
func listFingerprints(ctx context.Context, home string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "gpg", "--batch", "--with-colons", "--list-keys")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("anchor gpg list-keys: %w", err)
	}

	var fprs []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			parts := strings.Split(line, ":")
			if len(parts) > 9 && parts[9] != "" {
				fprs = append(fprs, parts[9])
			}
		}
	}
	return fprs, nil
}
