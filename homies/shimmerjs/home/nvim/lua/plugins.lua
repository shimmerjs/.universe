local lz = require('lze')

lz.load({
  -- Treesitter (highlighting is built-in with Neovim 0.10+)
  {
    'nvim-treesitter',
    event = 'DeferredUIEnter',
    after = function()
      vim.cmd.packadd('nvim-treesitter-textobjects')

      -- Textobjects config (new API — keymaps set manually below)
      require('nvim-treesitter-textobjects').setup({
        select = { lookahead = true },
        move = { set_jumps = true },
      })

      local select = require('nvim-treesitter-textobjects.select')
      local move = require('nvim-treesitter-textobjects.move')
      local swap = require('nvim-treesitter-textobjects.swap')
      local map = vim.keymap.set

      -- Select textobjects
      local select_maps = {
        ['af'] = '@function.outer', ['if'] = '@function.inner',
        ['ac'] = '@class.outer',    ['ic'] = '@class.inner',
        ['aa'] = '@parameter.outer', ['ia'] = '@parameter.inner',
        ['ai'] = '@conditional.outer', ['ii'] = '@conditional.inner',
        ['al'] = '@loop.outer',    ['il'] = '@loop.inner',
        ['ab'] = '@block.outer',   ['ib'] = '@block.inner',
      }
      for key, query in pairs(select_maps) do
        map({ 'x', 'o' }, key, function() select.select_textobject(query) end, { desc = query })
      end

      -- Move to textobjects
      local move_maps = {
        [']f'] = { move.goto_next_start, '@function.outer', 'Next function start' },
        [']c'] = { move.goto_next_start, '@class.outer', 'Next class start' },
        [']a'] = { move.goto_next_start, '@parameter.inner', 'Next parameter' },
        [']F'] = { move.goto_next_end, '@function.outer', 'Next function end' },
        [']C'] = { move.goto_next_end, '@class.outer', 'Next class end' },
        ['[f'] = { move.goto_previous_start, '@function.outer', 'Prev function start' },
        ['[c'] = { move.goto_previous_start, '@class.outer', 'Prev class start' },
        ['[a'] = { move.goto_previous_start, '@parameter.inner', 'Prev parameter' },
        ['[F'] = { move.goto_previous_end, '@function.outer', 'Prev function end' },
        ['[C'] = { move.goto_previous_end, '@class.outer', 'Prev class end' },
      }
      for key, spec in pairs(move_maps) do
        map({ 'n', 'x', 'o' }, key, function() spec[1](spec[2]) end, { desc = spec[3] })
      end

      -- Swap parameters
      map('n', '<leader>a', function() swap.swap_next('@parameter.inner') end, { desc = 'Swap next parameter' })
      map('n', '<leader>A', function() swap.swap_previous('@parameter.inner') end, { desc = 'Swap prev parameter' })

      -- Repeatable moves with ; and ,
      local ts_repeat = require('nvim-treesitter-textobjects.repeatable_move')
      map({ 'n', 'x', 'o' }, ';', ts_repeat.repeat_last_move_next)
      map({ 'n', 'x', 'o' }, ',', ts_repeat.repeat_last_move_previous)

      -- Incremental selection via treesitter
      local inc_node = nil
      map('n', '<C-space>', function()
        inc_node = vim.treesitter.get_node()
        if not inc_node then return end
        local sr, sc, er, ec = inc_node:range()
        vim.fn.setpos("'<", { 0, sr + 1, sc + 1, 0 })
        vim.fn.setpos("'>", { 0, er + 1, ec, 0 })
        vim.cmd('normal! gv')
      end, { desc = 'Init treesitter selection' })
      map('x', '<C-space>', function()
        if inc_node then
          inc_node = inc_node:parent() or inc_node
          local sr, sc, er, ec = inc_node:range()
          vim.fn.setpos("'<", { 0, sr + 1, sc + 1, 0 })
          vim.fn.setpos("'>", { 0, er + 1, ec, 0 })
          vim.cmd('normal! gv')
        end
      end, { desc = 'Expand treesitter selection' })
      map('x', '<bs>', function()
        if inc_node then
          local child = inc_node:child(0)
          if child then inc_node = child end
          local sr, sc, er, ec = inc_node:range()
          vim.fn.setpos("'<", { 0, sr + 1, sc + 1, 0 })
          vim.fn.setpos("'>", { 0, er + 1, ec, 0 })
          vim.cmd('normal! gv')
        end
      end, { desc = 'Shrink treesitter selection' })
    end,
  },

  -- Treesitter context (sticky function/class headers)
  {
    'nvim-treesitter-context',
    event = 'DeferredUIEnter',
    after = function()
      require('treesitter-context').setup({
        max_lines = 3,
        trim_scope = 'outer',
      })
      vim.keymap.set('n', '<leader>tc', function()
        require('treesitter-context').toggle()
      end, { desc = 'Toggle treesitter context' })
      vim.keymap.set('n', 'gC', function()
        require('treesitter-context').go_to_context(vim.v.count1)
      end, { desc = 'Jump to context' })
    end,
  },

  -- Completion
  {
    'blink.cmp',
    event = 'DeferredUIEnter',
    after = function()
      require('blink.cmp').setup({
        keymap = { preset = 'default' },
        completion = {
          documentation = { auto_show = true },
        },
        sources = {
          default = { 'lsp', 'path', 'buffer' },
        },
      })
    end,
  },

  -- Telescope
  {
    'telescope.nvim',
    cmd = 'Telescope',
    keys = {
      { '<leader>ff', function() require('telescope.builtin').find_files() end, desc = 'Find files' },
      { '<leader>fg', function() require('telescope.builtin').live_grep() end, desc = 'Grep' },
      { '<leader>fw', function() require('telescope.builtin').grep_string() end, desc = 'Grep word under cursor' },
      { '<leader>fb', function() require('telescope.builtin').buffers() end, desc = 'Buffers' },
      { '<leader>fh', function() require('telescope.builtin').help_tags() end, desc = 'Help tags' },
      { '<leader>fk', function() require('telescope.builtin').keymaps() end, desc = 'Keymaps' },
      { '<leader>fd', function() require('telescope.builtin').diagnostics() end, desc = 'Diagnostics' },
      { '<leader>fs', function() require('telescope.builtin').lsp_document_symbols() end, desc = 'Document symbols' },
      { '<leader>fW', function() require('telescope.builtin').lsp_dynamic_workspace_symbols() end, desc = 'Workspace symbols' },
      { '<leader>fo', function() require('telescope.builtin').oldfiles() end, desc = 'Old files' },
      { '<leader>fr', function() require('telescope').extensions.recent_files.pick() end, desc = 'Recent files' },
      { '<leader>gc', function() require('telescope.builtin').git_commits() end, desc = 'Git commits' },
      { '<leader>gB', function() require('telescope.builtin').git_branches() end, desc = 'Git branches' },
      { '<leader>gf', function() require('telescope.builtin').git_status() end, desc = 'Git status' },
      { '<leader>/', function() require('telescope.builtin').current_buffer_fuzzy_find() end, desc = 'Fuzzy find in buffer' },
      { '<leader>fa', function() require('telescope').extensions.switch.switch() end, desc = 'Switch alternate file' },
      { '<leader>gi', function() require('telescope').extensions.gh.issues() end, desc = 'GitHub issues' },
      { '<leader>gp', function() require('telescope').extensions.gh.pull_request() end, desc = 'GitHub PRs' },
      { '<leader>gg', function() require('telescope').extensions.gh.gist() end, desc = 'GitHub gists' },
      { '<leader>gR', function() require('telescope').extensions.gh.run() end, desc = 'GitHub Actions runs' },
      { '<leader>fu', function() require('telescope').extensions.undo.undo() end, desc = 'Undo tree' },
      { '<leader>fj', function() require('telescope').extensions.adjacent.adjacent() end, desc = 'Adjacent files' },
      { '<leader><leader>', function() require('telescope.builtin').resume() end, desc = 'Resume last picker' },
    },
    after = function()
      vim.cmd.packadd('plenary.nvim')
      vim.cmd.packadd('nvim-web-devicons')
      vim.cmd.packadd('telescope-fzf-native.nvim')
      vim.cmd.packadd('telescope-ui-select.nvim')
      vim.cmd.packadd('telescope-recent-files')
      vim.cmd.packadd('telescope-switch')
      vim.cmd.packadd('telescope-github.nvim')
      vim.cmd.packadd('telescope-undo.nvim')
      vim.cmd.packadd('adjacent-nvim')

      local telescope = require('telescope')
      local switch_matchers = require('telescope._extensions.switch.matcher')
      telescope.setup({
        defaults = {
          sorting_strategy = 'ascending',
          layout_config = {
            horizontal = { prompt_position = 'top' },
          },
          file_ignore_patterns = { 'node_modules', '.git/' },
          mappings = {
            i = {
              ['<C-j>'] = 'move_selection_next',
              ['<C-k>'] = 'move_selection_previous',
            },
          },
        },
        pickers = {
          find_files = { hidden = true },
          buffers = {
            sort_mru = true,
            mappings = {
              i = { ['<C-d>'] = 'delete_buffer' },
            },
          },
        },
        extensions = {
          fzf = {
            fuzzy = true,
            override_generic_sorter = true,
            override_file_sorter = true,
          },
          ['ui-select'] = {
            require('telescope.themes').get_dropdown(),
          },
          recent_files = {
            only_cwd = true,
          },
          undo = {
            side_by_side = true,
            layout_strategy = 'vertical',
            layout_config = { preview_height = 0.6 },
          },
          adjacent = {
            level = 1,
          },
          switch = {
            matchers = {
              switch_matchers.go_test,
              switch_matchers.go_impl,
              {
                name = 'nix test',
                from = '(.+)%.nix$',
                to = '%1_test.nix',
              },
              {
                name = 'tf tfvars',
                from = '(.+)%.tf$',
                to = '%1.tfvars',
              },
            },
          },
        },
      })

      telescope.load_extension('fzf')
      telescope.load_extension('ui-select')
      telescope.load_extension('recent_files')
      telescope.load_extension('switch')
      telescope.load_extension('gh')
      telescope.load_extension('undo')
      telescope.load_extension('adjacent')
    end,
  },

  -- Octo (GitHub PRs/issues in nvim)
  {
    'octo.nvim',
    cmd = 'Octo',
    after = function()
      require('octo').setup({
        use_local_fs = false,
        enable_builtin = true,
        default_remote = { 'upstream', 'origin' },
        picker = 'telescope',
      })
    end,
  },

  -- LSP (using native vim.lsp.config API — Neovim 0.11+)
  {
    'nvim-lspconfig',
    event = 'DeferredUIEnter',
    before = function()
      vim.api.nvim_create_autocmd('LspAttach', {
        group = vim.api.nvim_create_augroup('LspKeymaps', { clear = true }),
        callback = function(args)
          local map = function(mode, lhs, rhs, desc)
            vim.keymap.set(mode, lhs, rhs, { buffer = args.buf, desc = 'LSP: ' .. desc })
          end
          local ok, builtin = pcall(require, 'telescope.builtin')
          if ok then
            map('n', 'gd', builtin.lsp_definitions, 'Definition')
            map('n', 'gr', builtin.lsp_references, 'References')
            map('n', 'gi', builtin.lsp_implementations, 'Implementation')
          else
            map('n', 'gd', vim.lsp.buf.definition, 'Definition')
            map('n', 'gr', vim.lsp.buf.references, 'References')
            map('n', 'gi', vim.lsp.buf.implementation, 'Implementation')
          end
          map('n', 'gD', vim.lsp.buf.declaration, 'Declaration')
          map('n', 'K', vim.lsp.buf.hover, 'Hover')
          map('n', '<leader>rn', vim.lsp.buf.rename, 'Rename')
          map('n', '<leader>ca', vim.lsp.buf.code_action, 'Code action')
          map('n', '<leader>cf', function()
            vim.lsp.buf.format({ async = true })
          end, 'Format')
        end,
      })
    end,
    after = function()
      -- Ensure blink-cmp is loaded for enhanced LSP capabilities
      vim.cmd.packadd('blink.cmp')
      local capabilities
      local ok, blink = pcall(require, 'blink.cmp')
      if ok then
        capabilities = blink.get_lsp_capabilities()
      else
        capabilities = vim.lsp.protocol.make_client_capabilities()
      end

      local servers = {}

      -- Go
      if nixCats('go') then
        vim.lsp.config('gopls', {
          capabilities = capabilities,
          settings = {
            gopls = {
              analyses = { unusedparams = true, shadow = true },
              staticcheck = true,
              gofumpt = true,
            },
          },
        })
        table.insert(servers, 'gopls')
      end

      -- Nix
      if nixCats('nix') then
        local nixpkgs_expr = 'import <nixpkgs> {}'
        if nixCats.extra and nixCats.extra.nixdExtras then
          nixpkgs_expr = nixCats.extra.nixdExtras.nixpkgs or nixpkgs_expr
        end
        vim.lsp.config('nixd', {
          capabilities = capabilities,
          settings = {
            nixd = { nixpkgs = { expr = nixpkgs_expr } },
          },
        })
        table.insert(servers, 'nixd')
      end

      -- Lua (set up lazydev before lua_ls)
      if nixCats('lua') then
        vim.cmd.packadd('lazydev.nvim')
        require('lazydev').setup({})
        vim.lsp.config('lua_ls', {
          capabilities = capabilities,
          settings = {
            Lua = {
              runtime = { version = 'LuaJIT' },
              diagnostics = { globals = { 'vim', 'nixCats' } },
              workspace = { checkThirdParty = false },
              telemetry = { enable = false },
            },
          },
        })
        table.insert(servers, 'lua_ls')
      end

      -- Shell
      if nixCats('shell') then
        vim.lsp.config('bashls', {
          capabilities = capabilities,
          filetypes = { 'sh', 'bash', 'zsh' },
        })
        table.insert(servers, 'bashls')
      end

      -- Terraform
      if nixCats('tf') then
        vim.lsp.config('terraformls', { capabilities = capabilities })
        table.insert(servers, 'terraformls')
      end

      -- YAML
      if nixCats('yaml') then
        vim.lsp.config('yamlls', {
          capabilities = capabilities,
          settings = {
            yaml = { schemaStore = { enable = true }, validate = true },
          },
        })
        table.insert(servers, 'yamlls')
      end

      -- JSON
      if nixCats('json') then
        vim.lsp.config('jsonls', {
          capabilities = capabilities,
          settings = {
            json = { validate = { enable = true } },
          },
        })
        table.insert(servers, 'jsonls')
      end

      -- Markdown
      if nixCats('markdown') then
        vim.lsp.config('marksman', { capabilities = capabilities })
        table.insert(servers, 'marksman')
      end

      -- CUE
      if nixCats('cue') then
        vim.lsp.config('cue', { capabilities = capabilities })
        table.insert(servers, 'cue')
      end

      vim.lsp.enable(servers)
    end,
  },

  -- Mini modules
  {
    'mini.nvim',
    event = 'DeferredUIEnter',
    after = function()
      require('mini.ai').setup({
        custom_textobjects = {
          -- disable; treesitter textobjects owns these with AST precision
          f = false,
          a = false,
        },
      })
      require('mini.surround').setup()
      require('mini.pairs').setup()
    end,
  },

  -- Status line
  {
    'lualine.nvim',
    event = 'DeferredUIEnter',
    after = function()
      vim.cmd.packadd('lualine-lsp-progress')
      require('lualine').setup({
        options = {
          theme = 'everforest',
        },
        sections = {
          lualine_c = { 'filename', 'lsp_progress' },
        },
      })
    end,
  },

  -- Git signs
  {
    'gitsigns.nvim',
    event = 'DeferredUIEnter',
    after = function()
      require('gitsigns').setup({
        signs = {
          add = { text = '│' },
          change = { text = '│' },
          delete = { text = '_' },
          topdelete = { text = '‾' },
          changedelete = { text = '~' },
        },
        current_line_blame = false,
        on_attach = function(bufnr)
          local gs = package.loaded.gitsigns
          local map = function(mode, lhs, rhs, desc)
            vim.keymap.set(mode, lhs, rhs, { buffer = bufnr, desc = 'Git: ' .. desc })
          end

          -- Navigation (expr mappings for diff-mode fallback)
          vim.keymap.set('n', ']h', function()
            if vim.wo.diff then return ']c' end
            vim.schedule(function() gs.next_hunk() end)
            return '<Ignore>'
          end, { buffer = bufnr, expr = true, desc = 'Git: Next hunk' })
          vim.keymap.set('n', '[h', function()
            if vim.wo.diff then return '[c' end
            vim.schedule(function() gs.prev_hunk() end)
            return '<Ignore>'
          end, { buffer = bufnr, expr = true, desc = 'Git: Previous hunk' })

          -- Actions
          map('n', '<leader>hs', gs.stage_hunk, 'Stage hunk')
          map('n', '<leader>hr', gs.reset_hunk, 'Reset hunk')
          map('v', '<leader>hs', function() gs.stage_hunk({ vim.fn.line('.'), vim.fn.line('v') }) end, 'Stage hunk')
          map('v', '<leader>hr', function() gs.reset_hunk({ vim.fn.line('.'), vim.fn.line('v') }) end, 'Reset hunk')
          map('n', '<leader>hS', gs.stage_buffer, 'Stage buffer')
          map('n', '<leader>hu', gs.undo_stage_hunk, 'Undo stage hunk')
          map('n', '<leader>hR', gs.reset_buffer, 'Reset buffer')
          map('n', '<leader>hp', gs.preview_hunk, 'Preview hunk')
          map('n', '<leader>hb', function() gs.blame_line({ full = true }) end, 'Blame line')
          map('n', '<leader>tb', gs.toggle_current_line_blame, 'Toggle line blame')
          map('n', '<leader>hd', gs.diffthis, 'Diff this')
          map('n', '<leader>hD', function() gs.diffthis('~') end, 'Diff this ~')
          map('n', '<leader>td', gs.toggle_deleted, 'Toggle deleted')

          -- Text object
          map({ 'o', 'x' }, 'ih', ':<C-U>Gitsigns select_hunk<CR>', 'Select hunk')
        end,
      })
    end,
  },

  -- Which-key
  {
    'which-key.nvim',
    event = 'DeferredUIEnter',
    after = function()
      require('which-key').setup()
    end,
  },

  -- Startup time profiling
  {
    'vim-startuptime',
    cmd = 'StartupTime',
  },
})
