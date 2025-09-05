package common

import "time"

type CommitInfo struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Author    AuthorInfo `json:"author"`
	Committer AuthorInfo `json:"committer"`
	CreatedAt time.Time  `json:"created_at"`
}
