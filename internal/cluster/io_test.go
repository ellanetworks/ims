package cluster

import "io"

func copyAll(w io.Writer, r io.Reader) (int64, error) {
	return io.Copy(w, r)
}
