package logger

import (
	"log"
	"os"
)

func New() *log.Logger {
	return log.New(os.Stdout, "[wallet-tracker] ", log.LstdFlags|log.Lshortfile)
}
