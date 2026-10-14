# help

Read when: discovering command usage from the CLI itself.

`whatscli help` is the Cobra-provided help command. Every command also accepts `--help`.
Root help prints the hosted documentation URL, and `whatscli docs` prints it directly.

## Commands

```bash
whatscli help [command]
whatscli [command] --help
```

## Examples

```bash
whatscli help send
whatscli send text --help
whatscli send status --help
whatscli docs
whatscli groups participants add --help
```
