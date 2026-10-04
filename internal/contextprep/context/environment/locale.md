---
id: locale
description: The locale is not UTF-8, so tools may mangle non-ASCII text
when: {utf8: false}
---
The locale is not UTF-8 ({{locale}}): tools may print non-ASCII text as `?` or escapes, or reject it. Set LC_ALL to a UTF-8 locale (C.UTF-8 or en_US.UTF-8) for a command that reads or writes such text.
