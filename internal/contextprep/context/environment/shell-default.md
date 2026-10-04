---
id: shell-default
description: $SHELL and the login shell were unusable, so uah fell back to /bin/sh
when: {shell_source: [default]}
---
$SHELL was unset or not an executable file when uah started, and the login shell could not be read, so uah fell back to {{shell}}.
