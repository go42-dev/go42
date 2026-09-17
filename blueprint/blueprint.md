---
id: go42-blueprint
title: go42 blueprint
---

# go42 blueprint

## Purpose

This is set of instructions needed to setup newly created application repository from the go42 blueprint.

## Operations

Types of operations needed to complete the adoption of the go42 blueprint.

### Replace a value

Replace all field placeholder occurrences `{{.GO42_*}}` with the valid value.

- Placeholder names always start with `.GO42_` and are in uppercase.
- Placeholders can appear in any file, including code, configuration, and documentation.

### Replace a section

Put matching comments around content that needs application-specific prose:

```markdown
<!-- go42:replace:start id-go42-app-purpose -->

Describe the application's problem, intended users, and main responsibilities.

<!-- go42:replace:end id-go42-app-purpose -->
```

### Remove a section

```markdown
<!-- go42:remove:start id-go42-blueprint-setup -->

Fill in the project details and complete the adoption checklist.

<!-- go42:remove:end id-go42-blueprint-setup -->
```
