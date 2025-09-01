package responses

type BlobResponse struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}
