// keyboard nav:
//   - index:
//     - / = change focus to the search box
//     - j = select the next result in the search results
//     - k = select the previous result in the search results
//   - filepath:
//     - n = jump to next match in file
//     - shift + n = jump to previous match in file

function assert(truth, msg) {
	if (!truth) {
		throw new Error(msg);
	}
}

function wrapAround(n, min, max) {
	const range = max - min;
	return ((((n - min) % range) + range) % range) + min;
}

function initSearchInputNav() {
	const inputEl = document.getElementById("search-input");

	const handleWindowKeydown = (ev) => {
		if (ev.key === "/") {
			ev.preventDefault();
			inputEl.focus();
		}
	};

	const handleInputKeydown = (ev) => {
		if (ev.key === "Escape") {
			inputEl.blur();
		}
	};

	const handleInputFocus = (ev) => {
		window.removeEventListener("keydown", handleWindowKeydown);
		// NOTE: move cursor to end of line.
		inputEl.setSelectionRange(inputEl.value.length, inputEl.value.length);
	};

	const handleInputBlur = (ev) => {
		window.addEventListener("keydown", handleWindowKeydown);
	};

	window.addEventListener("keydown", handleWindowKeydown);

	inputEl.addEventListener("keydown", handleInputKeydown);
	inputEl.addEventListener("focus", handleInputFocus);
	inputEl.addEventListener("blur", handleInputBlur);
}
initSearchInputNav();

function initSearchResultsNav() {
	const searchResultEls = Array.from(document.querySelectorAll(`[id^="search-result-"]`));
	if (searchResultEls.length === 0) {
		return;
	}

	let lastFocusedIdx = null;

	function focusIdxRelative(relative) {
		const newIdx = wrapAround(
			(lastFocusedIdx ?? (relative > 0 ? -1 : searchResultEls.length)) + relative,
			0,
			searchResultEls.length,
		);
		const target = searchResultEls[newIdx];
		target.focus();
	}

	const handleWindowKeydown = (ev) => {
		if (ev.key.toLowerCase() === "j") {
			focusIdxRelative(1);
		} else if (ev.key.toLowerCase() === "k") {
			focusIdxRelative(-1);
		} else if (ev.key === "Escape") {
			searchResultEls[lastFocusedIdx]?.blur();
		}
	};

	const handleWindowFocusin = (ev) => {
		const focusedIdx = searchResultEls.indexOf(ev.target);
		if (focusedIdx !== -1) {
			lastFocusedIdx = focusedIdx;
		}
	};

	window.addEventListener("keydown", handleWindowKeydown);
	window.addEventListener("focusin", handleWindowFocusin);

	// for when we're viewing a file
	const maybeInitEl = searchResultEls.find((el) => el.innerText === location.pathname);
	if (maybeInitEl) {
		lastFocusedIdx = searchResultEls.indexOf(maybeInitEl);
		maybeInitEl.scrollIntoView();
	}
}
initSearchResultsNav();

function initSourceFileNav() {
	// NOTE: sfl stands for source-file-line
	const matchedEls = Array.from(document.querySelectorAll(`[data-sfl-matched="true"]`));
	if (matchedEls.length === 0) {
		return;
	}

	// NOTE: fsl stands for file-search-line
	const searchResultEl = document.querySelector(`[data-filename="${location.pathname}"]`);
	const searchResultMatchedEls = searchResultEl
		? Array.from(searchResultEl.querySelectorAll(`[data-fsl-matched="true"]`))
		: null;
	if (searchResultMatchedEls !== null) {
		assert(
			searchResultMatchedEls.length === matchedEls.length,
			`length missmatch (got ${searchResultMatchedEls.length}, want ${matchedEls.length})`
		);
	}

	const targetedLineNoEl = document.getElementById("targeted-line-no");

	const toPrevMatchButton = document.getElementById("to-prev-match");
	const toNextMatchButton = document.getElementById("to-next-match");

	function findTargetedEl() {
		const targetId = location.hash.substring(1);
		return matchedEls.find((el) => el.id == targetId);
	}

	function deactivateElAtIdx(idx) {
		const el = matchedEls[idx];
		el.classList.remove("source-line--selected");
		el.removeAttribute("tabIndex");

		const maybeSrEl = searchResultMatchedEls?.[idx];
		if (maybeSrEl) {
			maybeSrEl.classList.remove("source-line--selected");
		}
	}

	function activateElAtIdx(idx, shouldFocus) {
		const el = matchedEls[idx];
		el.classList.add("source-line--selected");
		el.setAttribute("tabIndex", "-1");
		el.scrollIntoView();
		if (shouldFocus) {
			el.focus();
		}

		targetedLineNoEl.innerText = `${idx + 1} /`;

		const maybeSrEl = searchResultMatchedEls?.[idx];
		if (maybeSrEl) {
			maybeSrEl.classList.add("source-line--selected");
		}
	}

	function jumpToIdxRelative(relative) {
		const prevEl = findTargetedEl();
		const prevElIdx = matchedEls.indexOf(prevEl) ?? -1;
		const nextElIdx = wrapAround(prevElIdx + relative, 0, matchedEls.length);
		const nextEl = matchedEls[nextElIdx];

		if (prevElIdx !== -1) {
			deactivateElAtIdx(prevElIdx, true);
		}
		activateElAtIdx(nextElIdx);

		history.replaceState(null, null, `#${nextEl.id}`);
	}

	toPrevMatchButton.disabled = false;
	toNextMatchButton.disabled = false;

	toPrevMatchButton.onclick = () => jumpToIdxRelative(-1);
	toNextMatchButton.onclick = () => jumpToIdxRelative(1);

	const handleWindowKeydown = (ev) => {
		if (ev.key.toLowerCase() === "n") {
			jumpToIdxRelative(ev.shiftKey ? -1 : 1);
		} else if (ev.key === "Escape") {
			findTargetedEl()?.blur();
		}
	};
	window.addEventListener("keydown", handleWindowKeydown);

	const targetedElIdx = matchedEls.indexOf(findTargetedEl());
	if (targetedElIdx !== -1) {
		activateElAtIdx(targetedElIdx, false);
	}
}
initSourceFileNav();

