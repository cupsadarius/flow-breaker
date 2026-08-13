package main

import "testing"

func TestFindTaskByID(t *testing.T) {
	s := &Store{Tasks: []Task{{ID: 1}, {ID: 7}}, NextID: 8}
	if _, err := s.findTaskByID(7); err != nil {
		t.Fatalf("want task 7, got err %v", err)
	}
	if _, err := s.findTaskByID(99); err == nil {
		t.Fatal("want error for missing id 99")
	}
}
