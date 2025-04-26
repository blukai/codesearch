// keyboard nav:
//   - h / l = move focus focus to left / right panel
//   - / = change focus to the search box
//   - n / shift + n = select next / previous result in the search results

function assert(truth, msg) {
	if (!truth) {
		throw new Error(msg);
	}
}

function wrapAround(n, min, max) {
	const range = max - min;
	return ((((n - min) % range) + range) % range) + min;
}

function activateSearchInputNav(inputElId) {
	const inputEl = document.getElementById(inputElId);

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

	return () => {
		window.removeEventListener("keydown", handleWindowKeydown);
		inputEl.removeEventListener("keydown", handleInputKeydown);
		inputEl.removeEventListener("focus", handleInputFocus);
		inputEl.removeEventListener("blur", handleInputBlur);
	};
}

function activateListNav(selector, { getLastFocusedIdx, setLastFocusedIdx }) {
	const listEls = Array.from(document.querySelectorAll(selector));

	function focusIdxRelative(relative) {
		const newIdx = wrapAround(
			(getLastFocusedIdx() ?? (relative > 0 ? -1 : listEls.length)) + relative,
			0,
			listEls.length,
		);
		const target = listEls[newIdx];
		target.focus();
	}

	const handleWindowKeydown = (ev) => {
		if (ev.target instanceof HTMLInputElement) {
			return;
		}
		if (ev.key.toLowerCase() === "n") {
			focusIdxRelative(ev.shiftKey ? -1 : 1);
		} else if (ev.key === "Escape") {
			listEls[getLastFocusedIdx()]?.blur();
		}
	};

	const handleWindowFocusin = (ev) => {
		const focusedIdx = listEls.indexOf(ev.target);
		if (focusedIdx !== -1) {
			setLastFocusedIdx(focusedIdx);
		}
	};

	window.addEventListener("keydown", handleWindowKeydown);
	window.addEventListener("focusin", handleWindowFocusin);

	return {
		listEls,
		deactivate: () => {
			window.removeEventListener("keydown", handleWindowKeydown);
			window.removeEventListener("focusin", handleWindowFocusin);
		},
	};
}

let lastFocusedSearchResultIdx = null;
let didActivateSearchResultsNavOnce = false;

function activateSearchResultsNav() {
	const getLastFocusedIdx = () => lastFocusedSearchResultIdx;
	const setLastFocusedIdx = (idx) => lastFocusedSearchResultIdx = idx;

	const { listEls, deactivate } = activateListNav(
		`[id^="search-result-"]`,
		{ getLastFocusedIdx, setLastFocusedIdx },
	);

	// for when we're viewing a file
	if (!didActivateSearchResultsNavOnce) {
		if (location.pathname !== "/") {
			const maybeInitListEl = listEls.find((el) => (
				el.getAttribute("href").startsWith(location.pathname)
			));
			if (maybeInitListEl) {
				setLastFocusedIdx(listEls.indexOf(maybeInitListEl));
				maybeInitListEl.scrollIntoView();
			}
		}
		didActivateSearchResultsNavOnce = true;
	}

	return deactivate;
}

let didActivateSourceFileNavOnce = false;

function activateSourceFileNav() {
	// NOTE: sfl stands for source-file-line
	const matchedEls = Array.from(document.querySelectorAll(`[data-sfl-matched="true"]`));
	if (matchedEls.length === 0) {
		return;
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
	}

	function activateElAtIdx(idx) {
		const el = matchedEls[idx];
		el.classList.add("source-line--selected");
		el.scrollIntoView();

		targetedLineNoEl.innerText = `${idx + 1} / ${matchedEls.length}`;
	}

	function jumpToIdxRelative(relative) {
		const prevEl = findTargetedEl();
		const prevElIdx = matchedEls.indexOf(prevEl) ?? -1;
		const nextElIdx = wrapAround(prevElIdx + relative, 0, matchedEls.length);
		const nextEl = matchedEls[nextElIdx];

		if (prevElIdx !== -1) {
			deactivateElAtIdx(prevElIdx);
		}
		activateElAtIdx(nextElIdx);

		history.replaceState(null, null, `#${nextEl.id}`);
	}

	toPrevMatchButton.disabled = false;
	toNextMatchButton.disabled = false;

	toPrevMatchButton.onclick = () => jumpToIdxRelative(-1);
	toNextMatchButton.onclick = () => jumpToIdxRelative(1);

	const handleWindowKeydown = (ev) => {
		if (ev.target instanceof HTMLInputElement) {
			return;
		}
		if (ev.key.toLowerCase() === "n") {
			jumpToIdxRelative(ev.shiftKey ? -1 : 1);
		} else if (ev.key === "Escape") {
			findTargetedEl()?.blur();
		}
	};

	window.addEventListener("keydown", handleWindowKeydown);

	if (!didActivateSourceFileNavOnce) {
		const targetedElIdx = matchedEls.indexOf(findTargetedEl() ?? matchedEls[0]);
		if (targetedElIdx !== -1) {
			activateElAtIdx(targetedElIdx);
		}
		didActivateSourceFileNavOnce = true;
	}

	return () => {
		window.removeEventListener("keydown", handleWindowKeydown);
	};
}

let lastFocusedSearchHistoryEntryIdx = null;

function activateSearchHistoryNav() {
	const getLastFocusedIdx = () => lastFocusedSearchHistoryEntryIdx;
	const setLastFocusedIdx = (idx) => lastFocusedSearchHistoryEntryIdx = idx;

	const { deactivate } = activateListNav(
		`[id^="search-history-entry-"]`,
		{ getLastFocusedIdx, setLastFocusedIdx },
	);

	return deactivate;
}

let lastFocusedSourceDirEntryIdx = null;

function activateSourceDirNav() {
	const getLastFocusedIdx = () => lastFocusedSourceDirEntryIdx;
	const setLastFocusedIdx = (idx) => lastFocusedSourceDirEntryIdx = idx;

	const { deactivate } = activateListNav(
		`[id^="source-dir-entry-"]`,
		{ getLastFocusedIdx, setLastFocusedIdx },
	);

	return deactivate;
}

function initPanelsNav() {
	const SPECIALS = {
		"panel-search-history": () => {
			const deactivators = [
				activateSearchHistoryNav(),
			];
			return () => {
				deactivators.forEach((deactivate) => deactivate?.());
			};
		},
		"panel-file-search": () => {
			const deactivators = [
				activateSearchInputNav("file-search-input"),
				activateSearchResultsNav(),
			];
			return () => {
				deactivators.forEach((deactivate) => deactivate?.());
			};
		},
		"panel-source-file": () => {
			const deactivators = [
				activateSourceFileNav(),
			];
			return () => {
				deactivators.forEach((deactivate) => deactivate?.());
			};
		},
		"panel-source-dir": () => {
			const deactivators = [
				activateSourceDirNav(),
			];
			return () => {
				deactivators.forEach((deactivate) => deactivate?.());
			};
		},
	};

	const panelEls = [
		"panel-search-history",
		"panel-file-search",
		"panel-source-file",
		"panel-source-dir",
	].map((id) => document.getElementById(id)).filter(Boolean);
	assert(panelEls.length > 0);

	let lastFocusedIdx = null;
	let deactivateSpecials = null;

	function deactivateLastFocusedEl() {
		const el = panelEls[lastFocusedIdx];
		el.classList.remove("panel--selected");

		el.removeAttribute("tabIndex");

		lastFocusedIdx = null;
		deactivateSpecials();
	}

	function activateElAtIdx(idx) {
		const el = panelEls[idx];
		el.classList.add("panel--selected");

		el.setAttribute("tabIndex", "-1");
		el.focus();

		lastFocusedIdx = idx;
		deactivateSpecials = SPECIALS[el.id]();
	}

	const handleKeydown = (ev) => {
		if (ev.target instanceof HTMLInputElement) {
			return;
		}
		if (ev.key.toLowerCase() === "l") {
			ev.preventDefault();
			const nextIdx = lastFocusedIdx + 1;
			if (nextIdx === panelEls.length) {
				return;
			}
			deactivateLastFocusedEl();
			activateElAtIdx(nextIdx);
		} else if (ev.key.toLowerCase() === "h") {
			ev.preventDefault();
			const nextIdx = lastFocusedIdx - 1;
			if (nextIdx === -1) {
				return;
			}
			deactivateLastFocusedEl();
			activateElAtIdx(nextIdx);
		}
	};

	let anyInputFocused = false;

	const handleFocus = (ev) => {
		anyInputFocused = ev.target instanceof HTMLInputElement;

		if (lastFocusedIdx === null) {
			return;
		}
		const targetIdx = panelEls.findIndex((el) => el.contains(ev.target));
		if (targetIdx === -1 || targetIdx === lastFocusedIdx) {
			return;
		}
		deactivateLastFocusedEl();
		activateElAtIdx(targetIdx);
	};

	const handleMousemove = (ev) => {
		if (anyInputFocused) {
			return;
		}
		handleFocus(ev);
	};

	window.addEventListener("keydown", handleKeydown);
	window.addEventListener("click", handleFocus);
	window.addEventListener("focusin", handleFocus);
	window.addEventListener("mousemove", handleMousemove);

	activateElAtIdx(0);
}
initPanelsNav();
