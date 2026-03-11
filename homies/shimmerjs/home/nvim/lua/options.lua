local g = vim.g
local o = vim.opt

g.mapleader = ' '
g.maplocalleader = ' '

-- Disable unused remote providers
g.loaded_node_provider = 0
g.loaded_perl_provider = 0
g.loaded_python3_provider = 0
g.loaded_ruby_provider = 0

o.number = true
o.relativenumber = true

o.expandtab = true
o.shiftwidth = 2
o.tabstop = 2
o.smartindent = true

o.ignorecase = true
o.smartcase = true
o.hlsearch = true
o.incsearch = true

o.termguicolors = true
o.signcolumn = 'yes'
o.cursorline = true
o.scrolloff = 8

o.splitright = true
o.splitbelow = true
o.foldmethod = 'expr'
o.foldexpr = 'v:lua.vim.treesitter.foldexpr()'
o.foldlevel = 99
o.foldlevelstart = 99
o.foldenable = true
o.undofile = true
o.swapfile = false
o.updatetime = 250
o.timeoutlen = 300
o.clipboard = 'unnamedplus'

-- match kitty's everforest_dark_soft theme
g.everforest_background = 'soft'
vim.cmd.colorscheme('everforest')
