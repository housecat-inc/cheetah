package main

import "github.com/housecat-inc/spacecat/pkg/run"

func main() {
	run.Run(map[string]string{
		"DATABASE_URL": "",
		"PORT":         "8080",
		"SPACE":        "",
	})
}
