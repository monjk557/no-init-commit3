package responses

type TreeEntry struct {
	Type string `json:"type"` // "tree" or "blob"
	Name string `json:"name"` // Entry name
	Path string `json:"path"` // Full path
}

type TreeEntriesResponse struct {
	Success bool        `json:"success"`
	Data    []TreeEntry `json:"data"`
}
