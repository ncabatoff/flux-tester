package main

import (
	"log"
	"os/user"
)

func getuser() user.User {
	u, err := user.Current()
	if err != nil {
		log.Fatalf("can't get current user: %v", err)
	}
	return *u
}

func username() string {
	return getuser().Username
}

func homedir() string {
	u := getuser()
	if u.HomeDir == "" {
		log.Fatal("user homedir is empty")
	}
	return u.HomeDir
}
