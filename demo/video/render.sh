#!/usr/bin/env bash
# Stitch the five clips into the 60 second demo: caption per shot, mp4 for
# sharing plus a README-friendly gif.
set -euo pipefail
cd "$(dirname "$0")/clips"

# Captions are pre-rendered PNG strips in captions/ (see the repo), overlaid
# at the bottom of the frame. The overlay filter exists in every ffmpeg
# build, unlike drawtext, which needs freetype compiled in.
caption() { # clip, caption png, out
  ffmpeg -y -loglevel error -i "$1" -i "../captions/$2" \
    -filter_complex "[0:v]fps=24,scale=1440:810[v];[v][1:v]overlay=0:H-h-24" -an "$3"
}

caption clip-1-healthy.webm c1.png c1.mp4
caption clip-2-breach.webm  c2.png c2.mp4
caption clip-3-pr.webm      c3.png c3.mp4
caption clip-4-merged.webm  c4.png c4.mp4
caption clip-5-healed.webm  c5.png c5.mp4

printf "file 'c%d.mp4'\n" 1 2 3 4 5 > list.txt
ffmpeg -y -loglevel error -f concat -safe 0 -i list.txt -c:v libx264 -crf 23 -pix_fmt yuv420p ../meshmedic-demo.mp4
ffmpeg -y -loglevel error -i ../meshmedic-demo.mp4 -vf "fps=10,scale=960:-1:flags=lanczos" ../meshmedic-demo.gif
du -h ../meshmedic-demo.mp4 ../meshmedic-demo.gif
