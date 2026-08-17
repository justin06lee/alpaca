;;; alpaca.el --- Run the alpaca chat TUI inside Emacs -*- lexical-binding: t; -*-

;; Author: justin06lee
;; URL: https://github.com/justin06lee/alpaca
;; Version: 0.1
;; Package-Requires: ((emacs "27.1"))
;; Keywords: terminals, comm

;; This file is not part of GNU Emacs.

;;; Commentary:

;; M-x alpaca opens the alpaca chat interface in a terminal buffer, so the
;; chat lives inside Emacs like everything else.  The TUI runs unmodified:
;; in `vterm' when it is installed — the full experience, mouse and
;; truecolor included — and in the built-in `term' otherwise, which is
;; entirely usable but renders fewer colors and ignores the mouse.
;;
;; Install with `make emacs-install' from the alpaca repository, which puts
;; the binary on PATH, copies this file into your Emacs configuration, and
;; adds autoloads to your init file.
;;
;;   M-x alpaca        the chat interface
;;   M-x alpaca-demo   the same interface against canned replies, no server
;;   M-x alpaca-serve  run the server in a buffer
;;
;; Calling a command again jumps to its existing buffer while the program is
;; still running, and restarts it after it has exited.

;;; Code:

(require 'term)

;; Declared special here so the let-bindings below stay dynamic even when
;; this file is byte-compiled on a machine without vterm.
(defvar vterm-shell)
(defvar vterm-buffer-name)
(declare-function vterm "vterm")

(defgroup alpaca nil
  "Run the alpaca chat TUI inside Emacs."
  :group 'terminals
  :prefix "alpaca-")

(defcustom alpaca-program "alpaca"
  "The alpaca binary.  A bare name is resolved against variable `exec-path'."
  :type 'string)

(defcustom alpaca-chat-arguments '("chat")
  "Arguments for the chat interface, e.g. (\"chat\" \"--profile\" \"work\")."
  :type '(repeat string))

(defcustom alpaca-prefer-vterm t
  "Use `vterm' for the interface when it is available.
vterm renders the TUI exactly as a real terminal would; setting this to nil
forces the built-in `term' even when vterm is installed."
  :type 'boolean)

(defun alpaca--command (args)
  "The full shell command for ARGS, each argument quoted."
  (mapconcat #'shell-quote-argument (cons alpaca-program args) " "))

(defun alpaca--live-p (buffer)
  "Whether BUFFER exists and its process is still running."
  (and (buffer-live-p buffer)
       (get-buffer-process buffer)
       (process-live-p (get-buffer-process buffer))))

(defun alpaca--run (name args)
  "Show the terminal buffer NAME running alpaca with ARGS.
Reuses the buffer while its program is alive; starts fresh otherwise."
  (unless (executable-find alpaca-program)
    (user-error "Cannot find `%s' on exec-path — run `make install' in the alpaca repo"
                alpaca-program))
  (let ((existing (get-buffer name)))
    (if (alpaca--live-p existing)
        (pop-to-buffer existing)
      (when (buffer-live-p existing)
        (kill-buffer existing))
      (if (and alpaca-prefer-vterm (require 'vterm nil t))
          ;; vterm reads its command from `vterm-shell' and its name from
          ;; `vterm-buffer-name' dynamically.
          (let ((vterm-shell (alpaca--command args))
                (vterm-buffer-name name))
            (vterm))
        (let ((buffer (apply #'term-ansi-make-term name alpaca-program nil args)))
          (with-current-buffer buffer
            (term-char-mode))
          (pop-to-buffer buffer))))))

;;;###autoload
(defun alpaca ()
  "Open the alpaca chat interface in a terminal buffer."
  (interactive)
  (alpaca--run "*alpaca*" alpaca-chat-arguments))

;;;###autoload
(defun alpaca-demo ()
  "Open the alpaca chat interface against canned replies — no server needed."
  (interactive)
  (alpaca--run "*alpaca-demo*" '("chat" "--demo")))

;;;###autoload
(defun alpaca-serve ()
  "Run `alpaca serve' in a buffer, so this machine serves its models."
  (interactive)
  (alpaca--run "*alpaca-serve*" '("serve")))

(provide 'alpaca)

;;; alpaca.el ends here
