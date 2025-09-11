package protocol

import (
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
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
	enc := packfile.NewEncoder(w, st, true) // ref-delta 사용 여부
	_, err := enc.Encode(hashes, 0)         // packWindow=0이면 델타 탐색 비활성화
	return err
}

// "packfile" 섹션 헤더 (pkt-line + flush)
func WritePackfileSectionHeader(w io.Writer) error {
	pw := &PktWriter{W: w, WithLF: false} // LF 안 붙임(길이 혼동 방지)
	if err := pw.WriteLine("packfile"); err != nil {
		return err
	}
	return pw.Flush() // 0000
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
	pw := &PktWriter{W: w, WithLF: true} // 광고는 사람 읽기 편하게 LF 포함으로 통일해도 OK
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
