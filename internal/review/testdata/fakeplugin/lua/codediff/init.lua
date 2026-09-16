-- Test double of the codediff.nvim setup module: keeps the options it was
-- given, or fails when LYNA_TMUX_TEST_SETUP_FAIL is set.
local M = {}

function M.setup(opts)
  if vim.env.LYNA_TMUX_TEST_SETUP_FAIL then
    error("fake setup failure")
  end
  M.options = opts
end

return M
