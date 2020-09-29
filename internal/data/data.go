package data

import "time"

type BranchList struct {
	Branches []Branch `json:"branches"`
}

type User struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"time"`
}

type Commit struct {
	Hash      string `json:"hash"`
	Message   string `json:"message"`
	Committer User   `json:"committer"`
	Author    User   `json:"author"`
}

type Branch struct {
	Name   string `json:"name"`
	Commit Commit `json:"commit"`
}
