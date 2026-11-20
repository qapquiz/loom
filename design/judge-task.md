You are the arena cross-judge for the loom M1 design candidates. Read these files under /home/nullphase/Projects/loom/: design/rubric.md, design/candidate-a.md, design/candidate-b.md, design/candidate-c.md.

Score every rubric criterion for every candidate, 0 to 2, with one line of evidence per score taken from the file text. Then recommend one base candidate and two to four specific grafts worth porting from the losing candidates, each naming the losing file and the section.

Note: the candidates are transcripts recovered from terminal panes, so lines are hard-wrapped and markdown headers may be plain text. Judge the design content, not the formatting.

Output your verdict as your final chat message in this exact shape, nothing else after it:

SCORES
criterion: a=X b=X c=X  (one line per criterion, each with a short evidence phrase)
BASE: <a|b|c> because <one line>
GRAFTS
- from candidate-<x>: <specific item> <why>
(repeat 2-4 times)

Do not write any files. Your final message is the artifact.
