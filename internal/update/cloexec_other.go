//go:build !unix

package update

func MarkCloseOnExec() int { return 0 }
