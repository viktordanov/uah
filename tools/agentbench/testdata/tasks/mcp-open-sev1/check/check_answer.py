"""ANSWER.md holds exactly the expected incident IDs, sorted, one per line."""
import sys

want = open("expected.txt").read().split()
try:
    got = [line.strip() for line in open("ANSWER.md") if line.strip()]
except FileNotFoundError:
    sys.exit("no ANSWER.md")
if got != want:
    sys.exit("ANSWER.md has %s, want %s" % (got, want))
