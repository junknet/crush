package game

import "testing"

func TestGameUpdate(t *testing.T) {
	g := NewGame(10, 10)
	initialHead := g.Snake[0]
	
	g.Direction = Right
	g.Update()
	
	newHead := g.Snake[0]
	if newHead.X != initialHead.X+1 {
		t.Errorf("Expected X to be %d, got %d", initialHead.X+1, newHead.X)
	}
}

func TestCollision(t *testing.T) {
	g := NewGame(10, 10)
	g.Snake = []Point{{X: 0, Y: 0}}
	g.Direction = Left
	g.Update()
	
	if !g.GameOver {
		t.Error("Expected GameOver to be true after hitting left wall")
	}
}
