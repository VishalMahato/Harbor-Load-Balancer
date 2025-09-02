package main

import (
	"fmt"

	"github.com/vishalmahato/harbor-load-balancer/internal/lb"
)

func main() {
	fmt.Println("Harbor Up")
	//  lets add seed data
	backends := []string{
		"127.0.0.1:9001",
		"127.0.0.1:9002",
	}
	// lets simulate calling backend
	for i := 0; i < 5; i++ {
		idx := lb.Next(len((backends))) //2
		fmt.Printf("Request %d -> %s\n", i, backends[idx])
	}
}
