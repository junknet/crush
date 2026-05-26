# Plan Mode Model Selection

- **Seamless Transition**: Entering plan mode via `shift+tab` is a state toggle (`toggleSessionMode`) and does not trigger a model selection UI.
- **Fallback Logic**: If `models.plan` is not explicitly configured in the config file, the system automatically falls back to the `models.brain` configuration.
- **Key Files**:
    - `internal/ui/model/session_mode.go`: Toggle logic.
    - `internal/config/config.go`: `SelectedModelForType` implements the fallback.
