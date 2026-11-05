**One long line ended the agent's event stream.** The stream reader used
`bufio.Scanner` with its default 64 KiB limit. Agent events carry whole tool
results, so a single large file read or diff made the scanner stop for the rest
of the run. The controller then lost the run's result event, its merge request
URL and the watchdog telemetry. Lines of any normal size are now read in full.
A line over 1 MiB is skipped with a warning, and reading continues.
The events kept for failure diagnosis now have a byte budget of 8 MiB per run,
so large events cannot use up the controller's memory.
