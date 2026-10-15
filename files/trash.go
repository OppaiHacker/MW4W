package main

// trashItem: one thing in the Recycle Bin. ID is how to restore or purge it, Path where its
// data is now (for previews).
type trashItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Orig    string `json:"orig"`
	Path    string `json:"path"`
	Deleted int64  `json:"deleted"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	Kind    string `json:"kind"`
}
