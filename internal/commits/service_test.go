package commits

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_convertToFileChange_UTF8Content_SetsContent(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationCreate,
		Content:   "hello",
		Encoding:  "utf-8",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.Equal(t, "a.txt", fc.Path)
	require.Equal(t, FileOperationCreate, fc.Operation)
	require.Equal(t, []byte("hello"), fc.Content)
}

func Test_convertToFileChange_EmptyEncoding_DefaultsToUTF8(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "b.txt",
		Operation: OperationUpdate,
		Content:   "world",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.Equal(t, []byte("world"), fc.Content)
}

func Test_convertToFileChange_Base64Content_DecodesContent(t *testing.T) {
	t.Parallel()

	raw := []byte("binary\x00data")
	op := &MultiFileOperation{
		Path:      "bin",
		Operation: OperationCreate,
		Content:   base64.StdEncoding.EncodeToString(raw),
		Encoding:  "base64",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.Equal(t, raw, fc.Content)
}

func Test_convertToFileChange_InvalidBase64_ReturnsError(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "x",
		Operation: OperationCreate,
		Content:   "not-valid-base64!!!",
		Encoding:  "base64",
	}

	_, err := convertToFileChange(op)

	require.Error(t, err)
	require.Contains(t, err.Error(), "base64")
}

func Test_convertToFileChange_UnsupportedEncoding_ReturnsError(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "x",
		Operation: OperationCreate,
		Content:   "x",
		Encoding:  "latin-1",
	}

	_, err := convertToFileChange(op)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported encoding")
}

func Test_convertToFileChange_Mode644_SetsFileMode(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "f.txt",
		Operation: OperationCreate,
		Content:   "x",
		Mode:      "100644",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.NotNil(t, fc.Mode)
}

func Test_convertToFileChange_Mode755_SetsFileMode(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "script",
		Operation: OperationCreate,
		Content:   "#!/bin/sh",
		Mode:      "755",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.NotNil(t, fc.Mode)
}

func Test_convertToFileChange_UnsupportedMode_ReturnsError(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:      "x",
		Operation: OperationCreate,
		Content:   "x",
		Mode:      "120000",
	}

	_, err := convertToFileChange(op)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported file mode")
}

func Test_convertToFileChange_Move_SetsOriginalPath(t *testing.T) {
	t.Parallel()

	op := &MultiFileOperation{
		Path:         "new.txt",
		Operation:    OperationMove,
		OriginalPath: "old.txt",
	}

	fc, err := convertToFileChange(op)

	require.NoError(t, err)
	require.Equal(t, "old.txt", fc.OriginalPath)
	require.Equal(t, FileOperationMove, fc.Operation)
}
