// keyboard nav:
//   - index:
//     - / = change focus to the search box
//     - n = select the next result in the search results
//     - shift + n = select the previous result in the search results
//   - filepath:
//     - n = jump to next match in file
//     - shift + n = jump to previous match in file

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
		if (ev.key.toLowerCase() === "n") {
			focusIdxRelative(ev.shiftKey ? -1 : 1);
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
}
initSearchResultsNav();

function initSourceFileNav() {
	const matchedLineEls = Array.from(document.querySelectorAll(`[data-matched="true"]`));
	if (matchedLineEls.length === 0) {
		return;
	}

	const targetedLineNoEl = document.getElementById("targeted-line-no");

	const toPrevMatchButton = document.getElementById("to-prev-match");
	const toNextMatchButton = document.getElementById("to-next-match");

	function findTargetedLineEl() {
		const targetId = location.hash.substring(1);
		return matchedLineEls.find((el) => el.id == targetId);
	}

	function updateTargetedMatchNo(maybeIdx = undefined) {
		const idx = maybeIdx ?? matchedLineEls.indexOf(findTargetedLineEl());
		if (idx === -1) {
			return;
		}
		targetedLineNoEl.innerText = `${idx + 1} /`;
		matchedLineEls[idx].classList.add("source-line--selected");
	}

	function jumpToIdxRelative(relative) {
		const prevEl = findTargetedLineEl();
		const prevElIdx = matchedLineEls.indexOf(prevEl) ?? -1;
		const nextElIdx = wrapAround(prevElIdx + relative, 0, matchedLineEls.length);
		const nextEl = matchedLineEls[nextElIdx];

		prevEl?.classList.remove("source-line--selected");
		updateTargetedMatchNo(nextElIdx);
		nextEl.scrollIntoView();

		history.replaceState(null, null, `#${nextEl.id}`);
	}

	updateTargetedMatchNo();

	toPrevMatchButton.disabled = false;
	toNextMatchButton.disabled = false;

	toPrevMatchButton.onclick = () => jumpToIdxRelative(-1);
	toNextMatchButton.onclick = () => jumpToIdxRelative(1);

	const handleWindowKeydown = (ev) => {
		if (ev.key.toLowerCase() === "n") {
			jumpToIdxRelative(ev.shiftKey ? -1 : 1);
		}
	};
	window.addEventListener("keydown", handleWindowKeydown);
}
initSourceFileNav();
