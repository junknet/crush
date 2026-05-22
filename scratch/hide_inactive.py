filepath = "mobile/crush_mobile/app/index.tsx"

with open(filepath, "r", encoding="utf-8") as f:
    lines = f.readlines()

# Let's verify line 3410 and 3411
target_line = "                                                         const isActiveSession = isActiveWorkspace && session.id === sessionID"

found = False
for idx, line in enumerate(lines):
    if line.strip() == target_line.strip() and idx > 3390 and idx < 3420:
        # Check if the next line is already the return check to avoid duplicate inserts
        if "if (!isActiveSession) return null" not in lines[idx+1]:
            # Insert the return statement on the next line
            # Determine indentation from current line
            indent = line[:len(line) - len(line.lstrip())]
            lines.insert(idx + 1, f"{indent}if (!isActiveSession) return null\n")
            found = True
            break

if found:
    with open(filepath, "w", encoding="utf-8") as f:
        f.writelines(lines)
    print("SUCCESS")
else:
    print("NOT FOUND OR ALREADY IMPLEMENTED")
