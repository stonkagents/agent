StonkAgents Windows Installer Assets
=====================================

Directory Structure
-------------------
icons/
  StonkAgents.ico           - Multi-size Windows icon (ready to use); referenced by the
                              WiX sources (StonkAgents.wxs Icon, ARP icon).
  stonkagents-16x16.png     - Individual PNGs for custom UI / WPF
  stonkagents-32x32.png
  stonkagents-48x48.png
  stonkagents-64x64.png     - Burn bundle LogoFile (StonkAgentsBundle.wxs)
  stonkagents-128x128.png
  stonkagents-256x256.png
  stonkagents-512x512.png
  stonkagents-1024x1024.png

wix-bitmaps/
  wix-dialog.bmp            - 493x312 WiX dialog background (Welcome/Finish pages)
  wix-banner.bmp            - 493x58 WiX banner (interior pages)
  wix-sidebar.png           - Burn bundle sidebar (LogoSideFile), 165x400 to match the
                              theme's ImageControl; regenerate with make-sidebar.mjs
                              (make-sidebar.py is the old Python version, reference only)
  dmg-background-source.png - Source image used to generate bitmaps (if regen needed)

svg-source/
  stonkagents-icon.svg      - Canonical vector source (1024x1024 viewBox)

Color Tokens (Use Exactly)
--------------------------
Background:     #080a0f   (Main window / void black)
Surface:        #0f1118   (Cards, panels)
Elevated:       #181c28   (Elevated surfaces)
Accent Green:   #00FF00   (Primary accent, buttons, glow)
Accent Red:     #FF4D4D   (Crab body color)
Accent Blue:    #4a9eff   (Secondary accent)
Text Primary:   #e8e8e8   (Main readable text)
Text Secondary: #888888   (Muted / description text)
Text Disabled:  #555555   (Hint text, pending steps)
Border Default: #1a1f2e   (Default borders)
Border Accent:  #00FF00   (Active / focus borders)

Font Stack (Windows)
--------------------
Cascadia Code, Consolas, Courier New, monospace

Logo Description
----------------
Red crab (#FF4D4D) with neon green (#00FF00) pixel sunglasses on a dark
circle background (#080a0f) with subtle green stroke border.
