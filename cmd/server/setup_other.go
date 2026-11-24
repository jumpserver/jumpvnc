//go:build !windows && !linux

package main

import "errors"

func initializePlatformService(string) error {
	return errors.New("service initialization requires Windows or Linux")
}
