package main

import (
	"fmt"
	"os"
)

var noColor = os.Getenv("NO_COLOR") != ""

func sgr(code, text string) string {
	if noColor {
		return text
	}
	return fmt.Sprintf("\033[%sm%s\033[0m", code, text)
}

func colorRed(s string) string    { return sgr("31", s) }
func colorGreen(s string) string  { return sgr("32", s) }
func colorYellow(s string) string { return sgr("33", s) }
func colorCyan(s string) string   { return sgr("36", s) }
func colorBold(s string) string   { return sgr("1", s) }
func colorDim(s string) string    { return sgr("2", s) }

func printLog(tag string, colorFn func(string) string, ts, preview, chLabel string) {
	prefix := ""
	if chLabel != "" {
		prefix = colorDim("["+chLabel+"]") + " "
	}
	fmt.Printf("\r\033[K    %s  %s  %s%s\n", colorFn(tag), colorDim(ts), prefix, preview)
}
