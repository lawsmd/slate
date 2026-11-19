//go:build !windows

package term

import "os"

func enableVT(*os.File) (func(), error) { return func() {}, nil }

func rawWrite(out *os.File, p []byte) (int, error) { return out.Write(p) }
