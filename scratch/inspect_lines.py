filepath = "mobile/crush_mobile/app/index.tsx"
with open(filepath, "r", encoding="utf-8") as f:
    lines = f.readlines()

for i in range(3370, 3390):
    if i < len(lines):
        print(f"{i+1}: {repr(lines[i])}")
