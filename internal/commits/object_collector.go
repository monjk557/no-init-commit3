package commits

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/memory"
)

// objectCollector buffers new git objects in memory and writes them as a single
// packfile on Flush, avoiding loose object creation for API commit operations.
type objectCollector struct {
	objects []plumbing.EncodedObject
	hashes  []plumbing.Hash
	seen    map[plumbing.Hash]struct{}
}

func newObjectCollector() *objectCollector {
	return &objectCollector{
		objects: make([]plumbing.EncodedObject, 0, 32),
		hashes:  make([]plumbing.Hash, 0, 32),
		seen:    make(map[plumbing.Hash]struct{}),
	}
}

func (c *objectCollector) add(obj plumbing.EncodedObject) plumbing.Hash {
	h := obj.Hash()
	if _, dup := c.seen[h]; dup {
		return h
	}
	c.seen[h] = struct{}{}
	c.objects = append(c.objects, obj)
	c.hashes = append(c.hashes, h)
	return h
}

// addBlob creates and buffers a blob object.
func (c *objectCollector) addBlob(content []byte) (plumbing.Hash, error) {
	obj := &plumbing.MemoryObject{}
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return c.add(obj), nil
}

// encodeTree encodes and buffers a tree object.
func (c *objectCollector) encodeTree(tree *object.Tree) (plumbing.Hash, error) {
	obj := &plumbing.MemoryObject{}
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return c.add(obj), nil
}

// encodeCommit encodes and buffers a commit object.
func (c *objectCollector) encodeCommit(commit *object.Commit) (plumbing.Hash, error) {
	obj := &plumbing.MemoryObject{}
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return c.add(obj), nil
}

// flush writes all buffered objects as a packfile.
// Falls back to loose objects when PackfileWriter is not available.
func (c *objectCollector) flush(st storer.EncodedObjectStorer) error {
	if len(c.objects) == 0 {
		return nil
	}
	pw, ok := st.(storer.PackfileWriter)
	if !ok {
		for _, obj := range c.objects {
			if _, err := st.SetEncodedObject(obj); err != nil {
				return err
			}
		}
		return nil
	}
	memSt := memory.NewStorage()
	for _, obj := range c.objects {
		if _, err := memSt.SetEncodedObject(obj); err != nil {
			return err
		}
	}
	w, err := pw.PackfileWriter()
	if err != nil {
		return err
	}
	enc := packfile.NewEncoder(w, memSt, false)
	if _, err := enc.Encode(c.hashes, 10); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
