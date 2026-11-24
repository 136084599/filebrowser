//go:build !windows

package main

import "fmt"

func chooseTask() (string, error)        { return "", fmt.Errorf("provide --task") }
func chooseDestination() (string, error) { return "", fmt.Errorf("provide --destination") }
func enableUTF8()                        {}
func waitBeforeExit()                    {}
