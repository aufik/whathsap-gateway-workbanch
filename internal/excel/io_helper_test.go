package excel

import "os"

func readAll(path string) ([]byte, error) { return os.ReadFile(path) }
