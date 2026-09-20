-- Test double of codediff.nvim. :CodeDiff records the arguments it received
-- (LYNA_TMUX_TEST_FARGS) and, when asked, the editor state the review runs
-- with (LYNA_TMUX_TEST_STATE), then quits; it raises instead when
-- LYNA_TMUX_TEST_CODEDIFF_FAIL is set. Files are written under a temporary
-- name and renamed so a poller never reads a partial file.

local function write(path, data)
  local tmp = path .. ".tmp"
  local f = assert(io.open(tmp, "wb"))
  f:write(data)
  f:close()
  assert(os.rename(tmp, path))
end

vim.api.nvim_create_user_command("CodeDiff", function(opts)
  if vim.env.LYNA_TMUX_TEST_CODEDIFF_FAIL then
    error("fake CodeDiff failure")
  end
  if vim.env.LYNA_TMUX_TEST_STATE then
    -- The groups the generated colorscheme sets, read back as Neovim
    -- resolved them, so a test sees the colors a review is drawn with.
    local groups = {}
    for _, name in ipairs({ "Normal", "Comment", "Keyword", "DiffAdd", "@keyword", "@function", "@string", "@type" }) do
      local ok, hl = pcall(vim.api.nvim_get_hl, 0, { name = name })
      if ok and hl then
        groups[name] = { fg = hl.fg or vim.NIL, bg = hl.bg or vim.NIL, bold = hl.bold or false, italic = hl.italic or false }
      end
    end
    write(vim.env.LYNA_TMUX_TEST_STATE, vim.json.encode({
      highlight_groups = groups,
      setup = require("codediff").options or vim.NIL,
      runtimepath = vim.opt.runtimepath:get(),
      packpath = vim.opt.packpath:get(),
      termguicolors = vim.o.termguicolors,
      background = vim.o.background,
      modeline = vim.o.modeline,
      exrc = vim.o.exrc,
      swapfile = vim.o.swapfile,
      shadafile = vim.o.shadafile,
      no_auto_install = vim.env.VSCODE_DIFF_NO_AUTO_INSTALL or vim.NIL,
      no_watcher_install = vim.env.CODEDIFF_WATCHER_NO_AUTO_INSTALL or vim.NIL,
      watcher_path = vim.env.CODEDIFF_WATCHER_PATH or vim.NIL,
      cwd = vim.fn.getcwd(),
    }))
  end
  write(vim.env.LYNA_TMUX_TEST_FARGS, vim.json.encode(opts.fargs))
  vim.cmd("qall!")
end, { nargs = "*", bang = true, range = true })
