Analyze this codebase as the brain agent and create/update **{{.Config.Options.InitializeAs}}** to help future agents work effectively in this repository.

**First**: Check if directory is empty or contains only config files. If so, stop and say "Directory appears empty or only contains config. Add source code first, then run this command to generate {{.Config.Options.InitializeAs}}."

**Goal**: Document what an agent needs to know to work in this codebase - commands, patterns, conventions, gotchas, overall architecture, how components fit together

**Discovery process**:

1. Read the repository root, then the local rule files and facade entrypoints that define the project shape.
2. Use the `agent` tool only when breadth justifies it; choose `role=explore` for read-only survey work and `role=worker` for mutations. Do not hardcode `agent` as the first step.
3. Identify project type from config files and directory structure.
4. Find build/test/lint commands from config files, scripts, Makefiles, or CI configs.
5. Read representative source files to understand code patterns, architecture, control/data flow.
6. If {{.Config.Options.InitializeAs}} exists, read and improve it.

**Content to include**:

- Essential commands (build, test, run, deploy, etc.) - whatever is relevant for this project
- Code organization and structure, application architecture and control/data flow
- Naming conventions and style patterns
- Testing approach and patterns
- Important gotchas or non-obvious patterns
- Any project-specific context from existing rule files

**Note:** LLM agents learn and adapt to their context as they obtain it, so mentioning obvious details they would immediately pick up from reading a file or two is actively detrimental. Keep the principles of progressive disclosure in mind and focus primarily on non-obvious knowledge that saves the agent from trial-and-error discovery: gotchas, implicit conventions, commands with surprising flags, and context that isn't self-evident from the code in a single file.

**Format**: Clear markdown sections. Use your judgment on structure based on what you find. Aim for completeness over brevity - include everything an agent would need to know.

**Critical**: Only document what you actually observe. Never invent commands, patterns, or conventions. If you can't find something, don't include it.
