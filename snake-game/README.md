# Snake Game

A simple CLI Snake game implemented in Go using Bubble Tea.

## Directory Structure
```
snake-game/
├── cmd/
│   └── snake/
│       └── main.go       # Entry point
├── internal/
│   ├── game/             # Core logic
│   │   ├── game.go
│   │   └── game_test.go
│   └── ui/               # TUI implementation
│       └── ui.go
├── go.mod
└── go.sum
```

## How to Run
```bash
go run cmd/snake/main.go
```

## How to Test
```bash
go test ./internal/game/...
```

## Controls
- **Arrow Keys / WASD**: Move snake
- **r**: Restart (when game over)
- **q / Ctrl+C**: Quit
