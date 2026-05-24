package logger

import (
	"log"
	"os"
)

func New() *log.Logger {
	return log.New(os.Stdout, "[walletra] ", log.LstdFlags|log.Lshortfile)
}
