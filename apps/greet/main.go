package main

//go:generate templ generate
//go:generate sqlc generate

import "github.com/housecat-inc/cheetah"

func main() {
	cheetah.Run(map[string]string{
		"PORT": "8080",
	})
}
