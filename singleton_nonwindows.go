//go:build !windows

package main

func acquireSingleInstance() (release func(), alreadyRunning bool) {
	return func() {}, false
}

func activateExistingWindow() {}
