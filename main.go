package main

import (
	"os"

	"github.com/elastic/beats/libbeat/beat"

	"github.com/osiriswd/rsbeat/beater"
)

func main() {
	err := beat.Run("rsbeat", "", beater.New)
	if err != nil {
		os.Exit(1)
	}
}
