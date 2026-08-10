package main

import "os"

func injectCI() {
	os.Open("/tmp/x")
}
