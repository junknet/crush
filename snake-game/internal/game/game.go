package game

import (
	"math/rand"
	"time"
)

type Point struct {
	X, Y int
}

type Direction int

const (
	Up Direction = iota
	Down
	Left
	Right
)

type Game struct {
	Width, Height int
	Snake         []Point
	Direction     Direction
	Food          Point
	Score         int
	GameOver      bool
	rand          *rand.Rand
}

func NewGame(width, height int) *Game {
	g := &Game{
		Width:     width,
		Height:    height,
		Snake:     []Point{{X: width / 2, Y: height / 2}},
		Direction: Right,
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	g.SpawnFood()
	return g
}

func (g *Game) SpawnFood() {
	for {
		newFood := Point{
			X: g.rand.Intn(g.Width),
			Y: g.rand.Intn(g.Height),
		}
		
		onSnake := false
		for _, p := range g.Snake {
			if p == newFood {
				onSnake = true
				break
			}
		}
		if !onSnake {
			g.Food = newFood
			break
		}
	}
}

func (g *Game) SetDirection(dir Direction) {
	if (g.Direction == Up && dir == Down) ||
		(g.Direction == Down && dir == Up) ||
		(g.Direction == Left && dir == Right) ||
		(g.Direction == Right && dir == Left) {
		return
	}
	g.Direction = dir
}

func (g *Game) Update() {
	if g.GameOver {
		return
	}

	head := g.Snake[0]
	newHead := head

	switch g.Direction {
	case Up:
		newHead.Y--
	case Down:
		newHead.Y++
	case Left:
		newHead.X--
	case Right:
		newHead.X++
	}

	if newHead.X < 0 || newHead.X >= g.Width || newHead.Y < 0 || newHead.Y >= g.Height {
		g.GameOver = true
		return
	}

	for _, p := range g.Snake {
		if p == newHead {
			g.GameOver = true
			return
		}
	}

	g.Snake = append([]Point{newHead}, g.Snake...)

	if newHead == g.Food {
		g.Score += 10
		g.SpawnFood()
	} else {
		g.Snake = g.Snake[:len(g.Snake)-1]
	}
}
