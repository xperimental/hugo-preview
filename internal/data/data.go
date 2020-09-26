package data

type BranchList struct {
	Branches []Branch `json:"branches"`
}

type Branch struct {
	Name       string `json:"name"`
	CommitHash string `json:"commitHash"`
}
